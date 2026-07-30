package core

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/OboardProject/oboard/internal/model"
)

func ValidateForwardProtocol(v model.ForwardProtocol) error {
	switch v {
	case model.ForwardProtocolTCP, model.ForwardProtocolUDP, model.ForwardProtocolTCPUDP:
		return nil
	default:
		return fmt.Errorf("unsupported forward protocol %q", v)
	}
}

func ValidateForwardBackend(v model.ForwardBackend) error {
	switch v {
	case model.ForwardBackendAuto, model.ForwardBackendRealm, model.ForwardBackendNFT, model.ForwardBackendBuiltin:
		return nil
	default:
		return fmt.Errorf("unsupported forward backend %q", v)
	}
}

func ValidateForwardProbeMode(v string) error {
	switch v {
	case "", "never", "apply", "periodic", "sampled", "periodic_sampled":
		return nil
	default:
		return fmt.Errorf("unsupported forward probe_mode %q", v)
	}
}

func BuildPortForwardPlan(version int64, server model.Server, servers []model.Server, forwards []model.PortForward) (model.PortForwardPlan, error) {
	byID := map[int64]model.Server{}
	for _, item := range servers {
		byID[item.ID] = item
	}
	rules := make([]model.PortForward, 0)
	for _, f := range forwards {
		if !f.Enabled || f.SourceServerID != server.ID {
			continue
		}
		if err := ValidateForwardProtocol(f.Protocol); err != nil {
			return model.PortForwardPlan{}, err
		}
		if err := ValidateForwardBackend(f.Backend); err != nil {
			return model.PortForwardPlan{}, err
		}
		if err := ValidateListenIP(f.ListenIP); err != nil {
			return model.PortForwardPlan{}, err
		}
		if err := ValidatePort(f.ListenPort); err != nil {
			return model.PortForwardPlan{}, fmt.Errorf("listen_port: %w", err)
		}
		if err := ValidatePort(f.TargetPort); err != nil {
			return model.PortForwardPlan{}, fmt.Errorf("target_port: %w", err)
		}
		if strings.TrimSpace(f.TargetAddress) == "" {
			target, ok := byID[f.TargetServerID]
			if !ok {
				return model.PortForwardPlan{}, fmt.Errorf("target server %d does not exist", f.TargetServerID)
			}
			address, err := proxyPathReachableServerAddress(server, target)
			if err != nil {
				return model.PortForwardPlan{}, fmt.Errorf("port forward %q: %w", f.Name, err)
			}
			f.TargetAddress = address
		}
		if err := ValidateAddressForIPStack(EffectiveIPStack(server), f.TargetAddress); err != nil {
			target := byID[f.TargetServerID]
			if _, reachableErr := validateReachableServerAddress(server, target, f.TargetAddress); reachableErr != nil {
				return model.PortForwardPlan{}, fmt.Errorf("port forward %q: %w", f.Name, reachableErr)
			}
		}
		if strings.TrimSpace(f.TargetAddress) == "" {
			return model.PortForwardPlan{}, fmt.Errorf("port forward %q target_address is empty", f.Name)
		}
		rules = append(rules, f)
	}
	sort.SliceStable(rules, func(i, j int) bool {
		if rules[i].Priority == rules[j].Priority {
			return rules[i].ID < rules[j].ID
		}
		return rules[i].Priority < rules[j].Priority
	})
	return model.PortForwardPlan{Version: version, Rules: rules}, nil
}

func ValidatePortForwards(servers []model.Server, forwards []model.PortForward) error {
	known := map[int64]bool{}
	for _, s := range servers {
		known[s.ID] = true
	}
	for _, f := range forwards {
		if !f.Enabled {
			continue
		}
		if f.SourceServerID == 0 || f.TargetServerID == 0 {
			return errors.New("source_server_id and target_server_id required")
		}
		if f.SourceServerID == f.TargetServerID {
			return errors.New("port forward source and target must be different")
		}
		if !known[f.SourceServerID] {
			return fmt.Errorf("source server %d does not exist", f.SourceServerID)
		}
		if !known[f.TargetServerID] {
			return fmt.Errorf("target server %d does not exist", f.TargetServerID)
		}
		if err := ValidateForwardProtocol(f.Protocol); err != nil {
			return err
		}
		if err := ValidateForwardBackend(f.Backend); err != nil {
			return err
		}
		if err := ValidateForwardProbeMode(f.ProbeMode); err != nil {
			return err
		}
		if err := ValidateListenIP(f.ListenIP); err != nil {
			return err
		}
		if err := ValidatePort(f.ListenPort); err != nil {
			return fmt.Errorf("listen_port: %w", err)
		}
		if err := ValidatePort(f.TargetPort); err != nil {
			return fmt.Errorf("target_port: %w", err)
		}
	}
	return validateListenResources(portForwardListenResources(forwards))
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
