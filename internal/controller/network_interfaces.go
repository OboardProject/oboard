package controller

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strings"

	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
)

const (
	maxNetworkInterfaces         = 256
	maxNetworkInterfaceAddresses = 32
)

func validateNetworkInterfacesTaskResult(raw string) error {
	var result struct {
		Interfaces []model.NetworkInterfaceInfo `json:"interfaces"`
	}
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return fmt.Errorf("decode network interface result: %w", err)
	}
	if len(result.Interfaces) > maxNetworkInterfaces {
		return fmt.Errorf("network interface count %d exceeds limit %d", len(result.Interfaces), maxNetworkInterfaces)
	}
	seenNames := make(map[string]struct{}, len(result.Interfaces))
	for _, iface := range result.Interfaces {
		iface.Name = strings.TrimSpace(iface.Name)
		if iface.Name == "" {
			return errors.New("network interface name is empty")
		}
		if err := core.ValidateNetworkInterfaceName(iface.Name); err != nil {
			return fmt.Errorf("network interface %q: %w", iface.Name, err)
		}
		if _, exists := seenNames[iface.Name]; exists {
			return fmt.Errorf("duplicate network interface %q", iface.Name)
		}
		seenNames[iface.Name] = struct{}{}
		if len(iface.Addresses) > maxNetworkInterfaceAddresses {
			return fmt.Errorf("network interface %q address count %d exceeds limit %d", iface.Name, len(iface.Addresses), maxNetworkInterfaceAddresses)
		}
		seenAddresses := make(map[string]struct{}, len(iface.Addresses))
		for _, value := range iface.Addresses {
			value = strings.TrimSpace(value)
			if _, err := netip.ParsePrefix(value); err != nil {
				return fmt.Errorf("network interface %q has invalid address %q", iface.Name, value)
			}
			if _, exists := seenAddresses[value]; exists {
				return fmt.Errorf("network interface %q has duplicate address %q", iface.Name, value)
			}
			seenAddresses[value] = struct{}{}
		}
	}
	return nil
}
