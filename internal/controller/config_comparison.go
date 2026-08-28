package controller

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

// DeploymentProjectionDigest is the identity of one server's managed desired
// state. Runtime traffic baselines, leases, plan versions, timestamps, and
// probe results are excluded so accounting cannot trigger a deployment.
type DeploymentProjectionDigest struct {
	Digest       string
	DataPlane    string
	Assets       string
	PortForwards string
	Tunnels      string
	SSH          string
	DNS          string
	MTU          string
}

func (s *Server) compareServerConfigState(ctx context.Context, serverID int64, cfg string) (core.ConfigComparison, error) {
	previous, err := s.lastControllerDesiredConfig(ctx, serverID)
	if errors.Is(err, sql.ErrNoRows) || previous == "" {
		return core.ConfigComparison{}, nil
	}
	if err != nil {
		return core.ConfigComparison{}, err
	}
	return core.CompareConfigSemantics(previous, cfg)
}

func (s *Server) lastControllerDesiredConfig(ctx context.Context, serverID int64) (string, error) {
	last, err := s.store.LastSuccessfulConfigTaskByServer(ctx, serverID)
	if err != nil {
		return "", err
	}
	return controllerDesiredConfigJSON(*last), nil
}

func controllerDesiredConfigJSON(task model.AgentTask) string {
	switch task.Type {
	case model.AgentTaskTypeApplyDeployment:
		var payload model.DeploymentTaskPayload
		if json.Unmarshal([]byte(task.PayloadJSON), &payload) == nil {
			return payload.Config.Config
		}
	case model.AgentTaskTypeApplyCoreConfig:
		var payload model.ApplyCoreConfigTaskPayload
		if json.Unmarshal([]byte(task.PayloadJSON), &payload) == nil {
			return payload.Config
		}
	}
	return ""
}

func (s *Server) serverConfigUnchanged(ctx context.Context, serverID int64, cfg string) (bool, error) {
	cmp, err := s.compareServerConfigState(ctx, serverID, cfg)
	if err != nil {
		return false, err
	}
	return cmp.DataPlaneEqual, nil
}

func (s *Server) currentDeploymentProjection(ctx context.Context, server model.Server, data store.FullRoutingConfig, forwards []model.PortForward, tunnels []model.Tunnel, ledger *core.ProxyPathPortLedger) (DeploymentProjectionDigest, error) {
	generated, err := s.generateServerCoreConfigInner(ctx, server, data, ledger, false)
	if err != nil {
		return DeploymentProjectionDigest{}, err
	}
	forwardPlan, err := core.BuildPortForwardPlan(0, server, data.Servers, forwards)
	if err != nil {
		return DeploymentProjectionDigest{}, err
	}
	tunnelPlan, err := core.BuildTunnelPlan(0, server, data.Servers, tunnels)
	if err != nil {
		return DeploymentProjectionDigest{}, err
	}
	bindings, pathBindings, _, err := s.runtimeAccessBindings(ctx, data)
	if err != nil {
		return DeploymentProjectionDigest{}, err
	}
	sshPlan, err := buildSSHInboundPlan(0, server, data, bindings, pathBindings, nil)
	if err != nil {
		return DeploymentProjectionDigest{}, err
	}
	dnsState, err := core.DNSConfigStateForServer(server.ID, data.DNSLists, data.ServerDNSPolicies)
	if err != nil {
		return DeploymentProjectionDigest{}, err
	}
	return projectionDigestFromParts(generated.Config, generated.Assets, forwardPlan, tunnelPlan, sshPlan, dnsState, server), nil
}

func lastDeploymentProjection(payload model.DeploymentTaskPayload, configJSON string, server model.Server) DeploymentProjectionDigest {
	if configJSON == "" {
		configJSON = payload.Config.Config
	}
	var dns *core.DNSConfigState
	if payload.DNSBenchmark != nil {
		dns = &core.DNSConfigState{
			Policy:        &model.ServerDNSPolicy{Revision: payload.DNSBenchmark.PolicyRevision, Strategy: ""},
			EncryptedList: &model.DNSList{ID: payload.DNSBenchmark.EncryptedListID, Revision: payload.DNSBenchmark.EncryptedListRevision},
			BootstrapList: &model.DNSList{ID: payload.DNSBenchmark.BootstrapListID, Revision: payload.DNSBenchmark.BootstrapListRevision},
		}
	}
	return projectionDigestFromParts(configJSON, payload.Config.Assets, payload.PortForwards, payload.Tunnels, payload.SSHInbounds, dns, server)
}

func projectionDigestFromParts(config string, assets []model.ManagedAssetReference, forwards model.PortForwardPlan, tunnels model.TunnelPlan, ssh model.SSHInboundPlan, dns *core.DNSConfigState, server model.Server) DeploymentProjectionDigest {
	dataPlane := ""
	if digest, err := core.SemanticConfigDigest(config); err == nil {
		dataPlane = digest.DataPlaneDigest
	}
	out := DeploymentProjectionDigest{
		DataPlane:    dataPlane,
		Assets:       canonicalSHA(assetIdentity(assets)),
		PortForwards: canonicalSHA(portForwardIdentity(forwards)),
		Tunnels:      canonicalSHA(tunnelIdentity(tunnels)),
		SSH:          sshInboundPlanDigest(ssh),
		DNS:          canonicalSHA(dnsIdentity(dns)),
		MTU:          canonicalSHA(mtuIdentity(server)),
	}
	out.Digest = canonicalSHA(map[string]string{
		"data_plane":    out.DataPlane,
		"assets":        out.Assets,
		"port_forwards": out.PortForwards,
		"tunnels":       out.Tunnels,
		"ssh":           out.SSH,
		"dns":           out.DNS,
		"mtu":           out.MTU,
	})
	return out
}

func (d DeploymentProjectionDigest) topologyEqual(other DeploymentProjectionDigest) bool {
	return d.Digest != "" && d.Digest == other.Digest
}

func assetIdentity(assets []model.ManagedAssetReference) any {
	type item struct {
		Kind     string `json:"kind"`
		ID       int64  `json:"id"`
		Revision string `json:"revision"`
	}
	out := make([]item, 0, len(assets))
	for _, asset := range assets {
		out = append(out, item{Kind: asset.Kind, ID: asset.ID, Revision: asset.Revision})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func portForwardIdentity(plan model.PortForwardPlan) any {
	type rule struct {
		ID             int64  `json:"id"`
		ListenIP       string `json:"listen_ip"`
		ListenPort     int    `json:"listen_port"`
		TargetAddress  string `json:"target_address"`
		TargetPort     int    `json:"target_port"`
		Protocol       string `json:"protocol"`
		Backend        string `json:"backend"`
		TrustedForward any    `json:"trusted_forward,omitempty"`
		Enabled        bool   `json:"enabled"`
		SourceServerID int64  `json:"source_server_id"`
		TargetServerID int64  `json:"target_server_id"`
		ConfigJSON     string `json:"config_json"`
	}
	out := make([]rule, 0, len(plan.Rules))
	for _, item := range plan.Rules {
		out = append(out, rule{
			ID: item.ID, ListenIP: item.ListenIP, ListenPort: item.ListenPort,
			TargetAddress: item.TargetAddress, TargetPort: item.TargetPort, Protocol: string(item.Protocol),
			Backend: string(item.Backend), TrustedForward: item.TrustedForward, Enabled: item.Enabled,
			SourceServerID: item.SourceServerID, TargetServerID: item.TargetServerID, ConfigJSON: item.ConfigJSON,
		})
	}
	return out
}

func tunnelIdentity(plan model.TunnelPlan) any {
	type tunnel struct {
		ID             int64  `json:"id"`
		Type           string `json:"type"`
		SourceServerID int64  `json:"source_server_id"`
		TargetServerID int64  `json:"target_server_id"`
		LocalAddress   string `json:"local_address"`
		PeerAddress    string `json:"peer_address"`
		ListenPort     int    `json:"listen_port"`
		TargetEndpoint string `json:"target_endpoint"`
		TargetPort     int    `json:"target_port"`
		ConfigJSON     string `json:"config_json"`
		Enabled        bool   `json:"enabled"`
	}
	out := make([]tunnel, 0, len(plan.Tunnels))
	for _, item := range plan.Tunnels {
		out = append(out, tunnel{
			ID: item.ID, Type: string(item.Type), SourceServerID: item.SourceServerID, TargetServerID: item.TargetServerID,
			LocalAddress: item.LocalAddress, PeerAddress: item.PeerAddress, ListenPort: item.ListenPort,
			TargetEndpoint: item.TargetEndpoint, TargetPort: item.TargetPort, ConfigJSON: item.ConfigJSON, Enabled: item.Enabled,
		})
	}
	return out
}

func dnsIdentity(state *core.DNSConfigState) any {
	if state == nil || state.Policy == nil {
		return map[string]any{}
	}
	encRev, bootRev := int64(0), int64(0)
	encID, bootID := int64(0), int64(0)
	if state.EncryptedList != nil {
		encID = state.EncryptedList.ID
		encRev = state.EncryptedList.Revision
	}
	if state.BootstrapList != nil {
		bootID = state.BootstrapList.ID
		bootRev = state.BootstrapList.Revision
	}
	return map[string]any{
		"policy_revision":         state.Policy.Revision,
		"encrypted_list_id":       encID,
		"encrypted_list_revision": encRev,
		"bootstrap_list_id":       bootID,
		"bootstrap_list_revision": bootRev,
	}
}

func mtuIdentity(server model.Server) any {
	if server.ID == 0 {
		return map[string]any{}
	}
	return map[string]any{
		"mode":           server.MTUMode,
		"value":          server.MTUValue,
		"probe_host":     server.MTUProbeHost,
		"probe_port":     server.MTUProbePort,
		"overhead_bytes": server.MTUOverheadBytes,
	}
}

func canonicalSHA(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%x", sha256.Sum256(encoded))
}

func projectionTrafficOnly(previous, next string) bool {
	cmp, err := core.CompareConfigSemantics(previous, next)
	if err != nil {
		return false
	}
	return cmp.DataPlaneEqual && !cmp.ExactEqual
}
