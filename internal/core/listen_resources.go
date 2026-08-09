package core

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"strconv"
	"strings"

	"github.com/OboardProject/oboard/internal/model"
)

type listenTransport uint8

const (
	listenTCP listenTransport = 1 << iota
	listenUDP
)

type listenResource struct {
	serverID int64
	address  string
	port     int
	protocol listenTransport
	owner    string
}

func (r listenResource) conflicts(other listenResource) bool {
	return r.serverID == other.serverID && r.port == other.port && r.protocol&other.protocol != 0 && listenAddressesOverlap(r.address, other.address)
}

func validateListenResources(resources []listenResource) error {
	for i := range resources {
		for j := 0; j < i; j++ {
			if !resources[i].conflicts(resources[j]) {
				continue
			}
			return fmt.Errorf("%w: listen resource conflict on server %d (%s %s:%d): %s conflicts with %s",
				ErrInvalidDesiredState,
				resources[i].serverID,
				listenTransportName(resources[i].protocol&resources[j].protocol),
				normalizeListenAddress(resources[i].address),
				resources[i].port,
				resources[i].owner,
				resources[j].owner,
			)
		}
	}
	return nil
}

func listenAddressesOverlap(left, right string) bool {
	left = normalizeListenAddress(left)
	right = normalizeListenAddress(right)
	if left == "*" || right == "*" {
		return true
	}
	leftAddr, leftErr := netip.ParseAddr(left)
	rightAddr, rightErr := netip.ParseAddr(right)
	if leftErr != nil || rightErr != nil {
		return left == right
	}
	leftAddr = leftAddr.Unmap()
	rightAddr = rightAddr.Unmap()
	if leftAddr.IsUnspecified() && rightAddr.IsUnspecified() {
		return true
	}
	if leftAddr.IsUnspecified() {
		// A wildcard IPv6 socket is commonly dual stack on Linux. Treat it as
		// overlapping both families so a deployment never depends on sysctl or
		// runtime-specific IPV6_V6ONLY behavior.
		return leftAddr.Is6() || rightAddr.Is4()
	}
	if rightAddr.IsUnspecified() {
		return rightAddr.Is6() || leftAddr.Is4()
	}
	return leftAddr == rightAddr
}

func normalizeListenAddress(address string) string {
	address = strings.TrimSpace(address)
	if address == "" {
		return "0.0.0.0"
	}
	return address
}

func listenTransportName(protocol listenTransport) string {
	switch protocol {
	case listenTCP:
		return "tcp"
	case listenUDP:
		return "udp"
	default:
		return "tcp+udp"
	}
}

func singBoxInboundListenTransport(inbound map[string]any) listenTransport {
	switch strings.ToLower(strings.TrimSpace(stringFromAny(inbound["type"]))) {
	case "hysteria2", "hy2", "tuic":
		return listenUDP
	case "shadowsocks", "socks":
		switch strings.ToLower(strings.TrimSpace(stringFromAny(inbound["network"]))) {
		case "tcp":
			return listenTCP
		case "udp":
			return listenUDP
		default:
			return listenTCP | listenUDP
		}
	case "mieru":
		if strings.EqualFold(strings.TrimSpace(stringFromAny(inbound["transport"])), "udp") {
			return listenUDP
		}
		return listenTCP
	default:
		return listenTCP
	}
}

func singBoxListenResources(serverID int64, inbounds []map[string]any) []listenResource {
	resources := make([]listenResource, 0, len(inbounds))
	for index, inbound := range inbounds {
		ports := []int{intFromAny(inbound["listen_port"])}
		if strings.EqualFold(strings.TrimSpace(stringFromAny(inbound["type"])), "mieru") {
			expanded, err := mieruPortsFromValue(ports[0], inbound["listen_ports"])
			if err != nil {
				continue
			}
			ports = expanded
		}
		tag := strings.TrimSpace(stringFromAny(inbound["tag"]))
		if tag == "" {
			tag = strconv.Itoa(index)
		}
		for _, port := range ports {
			if !validPort(port) {
				continue
			}
			resources = append(resources, listenResource{
				serverID: serverID,
				address:  normalizeListenAddress(stringFromAny(inbound["listen"])),
				port:     port,
				protocol: singBoxInboundListenTransport(inbound),
				owner:    "core inbound " + tag,
			})
		}
	}
	return resources
}

func trustedForwardListenResources(serverID int64, trusted *OBoardTrustedForward) []listenResource {
	if trusted == nil {
		return nil
	}
	resources := make([]listenResource, 0, len(trusted.Receivers))
	for _, receiver := range trusted.Receivers {
		if !validPort(receiver.ListenPort) {
			continue
		}
		resources = append(resources, listenResource{
			serverID: serverID,
			address:  normalizeListenAddress(receiver.Listen),
			port:     receiver.ListenPort,
			protocol: portForwardListenTransport(model.ForwardProtocol(receiver.Network)),
			owner:    fmt.Sprintf("trusted forward receiver %q", receiver.ID),
		})
	}
	return resources
}

// inboundListenResource converts one persisted inbound into the same listen
// resource model deployment validation uses, so allocation and final
// validation share one conflict predicate (address scope, port, TCP/UDP).
func inboundListenResource(inbound model.Inbound) listenResource {
	return listenResource{
		serverID: inbound.ServerID,
		address:  normalizeListenAddress(inbound.ListenIP),
		port:     inbound.Port,
		protocol: portForwardListenTransport(transparentForwardProtocol(inbound)),
		owner:    fmt.Sprintf("inbound %q (id=%d)", inbound.Name, inbound.ID),
	}
}

func portForwardListenTransport(protocol model.ForwardProtocol) listenTransport {
	switch protocol {
	case model.ForwardProtocolTCP:
		return listenTCP
	case model.ForwardProtocolUDP:
		return listenUDP
	default:
		return listenTCP | listenUDP
	}
}

func portForwardListenResources(forwards []model.PortForward) []listenResource {
	resources := make([]listenResource, 0, len(forwards))
	for _, forward := range forwards {
		if !forward.Enabled || !validPort(forward.ListenPort) {
			continue
		}
		resources = append(resources, listenResource{
			serverID: forward.SourceServerID,
			address:  normalizeListenAddress(forward.ListenIP),
			port:     forward.ListenPort,
			protocol: portForwardListenTransport(forward.Protocol),
			owner:    fmt.Sprintf("port forward %q (id=%d)", forward.Name, forward.ID),
		})
	}
	return resources
}

func tunnelListenResources(serverID int64, tunnels []model.Tunnel) []listenResource {
	resources := make([]listenResource, 0, len(tunnels))
	for _, tunnel := range tunnels {
		if !tunnel.Enabled {
			continue
		}
		switch tunnel.Type {
		case model.TunnelTypeWireGuard:
			if validPort(tunnel.ListenPort) {
				resources = append(resources, listenResource{serverID: serverID, address: "*", port: tunnel.ListenPort, protocol: listenUDP, owner: fmt.Sprintf("WireGuard tunnel %q (id=%d)", tunnel.Name, tunnel.ID)})
			}
		case model.TunnelTypeSSH:
			var cfg struct {
				ManagedPair  bool   `json:"managed_pair"`
				Role         string `json:"role"`
				ServerPort   int    `json:"server_port"`
				LocalForward string `json:"local_forward"`
			}
			_ = json.Unmarshal([]byte(firstNonEmpty(tunnel.ConfigJSON, "{}")), &cfg)
			if cfg.ManagedPair && cfg.Role == "server" && validPort(cfg.ServerPort) {
				resources = append(resources, listenResource{serverID: serverID, address: "*", port: cfg.ServerPort, protocol: listenTCP, owner: fmt.Sprintf("managed SSH tunnel server %q (id=%d)", tunnel.Name, tunnel.ID)})
			}
			if address, port, ok := sshLocalForwardListen(cfg.LocalForward); ok {
				resources = append(resources, listenResource{serverID: serverID, address: address, port: port, protocol: listenTCP, owner: fmt.Sprintf("SSH local forward %q (id=%d)", tunnel.Name, tunnel.ID)})
			}
		}
	}
	return resources
}

func sshLocalForwardListen(spec string) (string, int, bool) {
	parts := strings.Split(strings.TrimSpace(spec), ":")
	address := "127.0.0.1"
	portIndex := 0
	if len(parts) == 4 {
		address = normalizeListenAddress(parts[0])
		portIndex = 1
	} else if len(parts) != 3 {
		return "", 0, false
	}
	port, err := strconv.Atoi(parts[portIndex])
	if err != nil || !validPort(port) {
		return "", 0, false
	}
	return address, port, true
}

func sshInboundListenResources(serverID int64, plan model.SSHInboundPlan) []listenResource {
	resources := make([]listenResource, 0, len(plan.Inbounds))
	for _, inbound := range plan.Inbounds {
		if !inbound.Enabled || !validPort(inbound.Port) {
			continue
		}
		resources = append(resources, listenResource{
			serverID: serverID,
			address:  normalizeListenAddress(inbound.ListenIP),
			port:     inbound.Port,
			protocol: listenTCP,
			owner:    fmt.Sprintf("SSH inbound %q (id=%d)", inbound.Name, inbound.InboundID),
		})
	}
	return resources
}

// ValidateDeploymentListenResources checks every listener that one Agent task
// will own before the task is queued. This is the final cross-component guard;
// individual resource validators still provide earlier, narrower feedback.
func ValidateDeploymentListenResources(serverID int64, configJSON string, forwards model.PortForwardPlan, tunnels model.TunnelPlan, sshInbounds model.SSHInboundPlan) error {
	var config SingBoxConfig
	if err := json.Unmarshal([]byte(configJSON), &config); err != nil {
		return fmt.Errorf("decode generated core config for listen validation: %w", err)
	}
	resources := singBoxListenResources(serverID, config.Inbounds)
	if config.OBoard != nil {
		resources = append(resources, trustedForwardListenResources(serverID, config.OBoard.TrustedForward)...)
	}
	resources = append(resources, portForwardListenResources(forwards.Rules)...)
	resources = append(resources, tunnelListenResources(serverID, tunnels.Tunnels)...)
	resources = append(resources, sshInboundListenResources(serverID, sshInbounds)...)
	return validateListenResources(resources)
}
