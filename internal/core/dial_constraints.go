package core

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"strings"

	"github.com/OboardProject/oboard/internal/model"
)

// DialConstraintMode enumerates how a physical dial should be bound.
const (
	DialConstraintModeAuto      = "auto"
	DialConstraintModeInterface = "interface"
)

// DialConstraintFamily enumerates the address family constraint for the
// underlay socket. It is distinct from the WARP traffic family (the
// addresses allowed inside the tunnel).
const (
	DialConstraintFamilyAuto     = "auto"
	DialConstraintFamilyIPv4Only = "ipv4_only"
	DialConstraintFamilyIPv6Only = "ipv6_only"
)

// DialConstraint describes the host network binding for the socket that
// actually opens the connection (the Physical Dial Owner). It is NOT a hop
// in a proxy path.
type DialConstraint struct {
	Mode          string `json:"mode"`
	InterfaceName string `json:"interface_name,omitempty"`
	SourceAddress string `json:"source_address,omitempty"`
	Family        string `json:"family,omitempty"`
}

// NormalizedDialConstraint is the validated, canonical form.
type NormalizedDialConstraint struct {
	Mode          string
	InterfaceName string
	SourceAddress string // canonical string, empty if not set
	Family        string
	SourcePrefix  netip.Prefix // valid only when SourceAddress != ""
}

// ValidateDialConstraint validates and normalizes a dial constraint document.
// Empty raw (`""` or `"{}"`) is treated as `auto`.
func ValidateDialConstraint(raw string) (*NormalizedDialConstraint, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "{}" {
		return &NormalizedDialConstraint{Mode: DialConstraintModeAuto, Family: DialConstraintFamilyAuto}, nil
	}
	var dc DialConstraint
	if err := json.Unmarshal([]byte(raw), &dc); err != nil {
		return nil, &ConfigFieldError{Path: "dial_constraint", Problem: err.Error()}
	}
	return ValidateDialConstraintObject(dc)
}

// ValidateDialConstraintObject validates a parsed DialConstraint.
func ValidateDialConstraintObject(dc DialConstraint) (*NormalizedDialConstraint, error) {
	mode := strings.ToLower(strings.TrimSpace(dc.Mode))
	if mode == "" {
		mode = DialConstraintModeAuto
	}
	if mode != DialConstraintModeAuto && mode != DialConstraintModeInterface {
		return nil, &ConfigFieldError{Path: "dial_constraint.mode", Problem: fmt.Sprintf("must be %q or %q", DialConstraintModeAuto, DialConstraintModeInterface)}
	}
	family := strings.ToLower(strings.TrimSpace(dc.Family))
	if family == "" {
		family = DialConstraintFamilyAuto
	}
	if family != DialConstraintFamilyAuto && family != DialConstraintFamilyIPv4Only && family != DialConstraintFamilyIPv6Only {
		return nil, &ConfigFieldError{Path: "dial_constraint.family", Problem: fmt.Sprintf("must be %q, %q or %q", DialConstraintFamilyAuto, DialConstraintFamilyIPv4Only, DialConstraintFamilyIPv6Only)}
	}
	iface := strings.TrimSpace(dc.InterfaceName)
	sourceStr := strings.TrimSpace(dc.SourceAddress)

	if mode == DialConstraintModeAuto {
		if iface != "" {
			return nil, &ConfigFieldError{Path: "dial_constraint.interface_name", Problem: "must be empty when mode is auto"}
		}
		if sourceStr != "" {
			return nil, &ConfigFieldError{Path: "dial_constraint.source_address", Problem: "must be empty when mode is auto"}
		}
		if family != DialConstraintFamilyAuto {
			return nil, &ConfigFieldError{Path: "dial_constraint.family", Problem: "must be auto when mode is auto"}
		}
		return &NormalizedDialConstraint{Mode: DialConstraintModeAuto, Family: DialConstraintFamilyAuto}, nil
	}
	// mode == interface : at least one of interface_name or source_address should be present.
	// Allow both together. Family may narrow the socket's DNS resolution.
	if iface == "" && sourceStr == "" {
		return nil, &ConfigFieldError{Path: "dial_constraint.interface_name", Problem: "interface_name or source_address is required when mode is interface"}
	}
	if iface != "" {
		if err := ValidateNetworkInterfaceName(iface); err != nil {
			return nil, &ConfigFieldError{Path: "dial_constraint.interface_name", Problem: err.Error()}
		}
	}
	var prefix netip.Prefix
	var canonicalSource string
	if sourceStr != "" {
		// Accept both address and prefix forms; normalize to address string.
		if addr, err := netip.ParseAddr(sourceStr); err == nil {
			if !addr.IsValid() || addr.IsUnspecified() || addr.IsLoopback() {
				return nil, &ConfigFieldError{Path: "dial_constraint.source_address", Problem: "must be a valid unicast address"}
			}
			// Preserve original textual family for family validation.
			prefix = netip.PrefixFrom(addr, addr.BitLen())
			canonicalSource = addr.String()
		} else if p, err := netip.ParsePrefix(sourceStr); err == nil {
			if !p.IsValid() || !p.Addr().IsValid() {
				return nil, &ConfigFieldError{Path: "dial_constraint.source_address", Problem: "must be a valid unicast address"}
			}
			prefix = p
			canonicalSource = p.Addr().String()
		} else {
			return nil, &ConfigFieldError{Path: "dial_constraint.source_address", Problem: "must be a valid IP address"}
		}
		// Family vs source address consistency.
		if family == DialConstraintFamilyIPv4Only && prefix.Addr().Is6() {
			return nil, &ConfigFieldError{Path: "dial_constraint.family", Problem: "family ipv4_only conflicts with IPv6 source_address"}
		}
		if family == DialConstraintFamilyIPv6Only && prefix.Addr().Is4() {
			return nil, &ConfigFieldError{Path: "dial_constraint.family", Problem: "family ipv6_only conflicts with IPv4 source_address"}
		}
	}
	return &NormalizedDialConstraint{
		Mode:          DialConstraintModeInterface,
		InterfaceName: iface,
		SourceAddress: canonicalSource,
		Family:        family,
		SourcePrefix:  prefix,
	}, nil
}

// MarshalDialConstraint returns the canonical JSON for storage.
func MarshalDialConstraint(dc *NormalizedDialConstraint) string {
	if dc == nil || dc.Mode == DialConstraintModeAuto {
		return "{}"
	}
	raw := DialConstraint{
		Mode:          dc.Mode,
		InterfaceName: dc.InterfaceName,
		SourceAddress: dc.SourceAddress,
		Family:        dc.Family,
	}
	if raw.Family == DialConstraintFamilyAuto {
		raw.Family = ""
	}
	b, _ := json.Marshal(raw)
	return string(b)
}

// ParseWARPUnderlay is an alias for WARP-specific validation that also
// rejects detour + underlay combinations at a higher layer.
func ParseWARPUnderlay(raw string) (*NormalizedDialConstraint, error) {
	return ValidateDialConstraint(raw)
}

// ApplyDialConstraintToEndpoint applies a normalized dial constraint to a
// sing-box WireGuard endpoint map. It enforces the Physical Dial Owner rule:
// if the endpoint already has a detour, binding fields are forbidden.
func ApplyDialConstraintToEndpoint(endpoint map[string]any, dc *NormalizedDialConstraint) error {
	if dc == nil || dc.Mode == DialConstraintModeAuto {
		return nil
	}
	if endpoint == nil {
		return fmt.Errorf("endpoint is nil")
	}
	if detour, _ := endpoint["detour"].(string); strings.TrimSpace(detour) != "" {
		return markInvalidDesiredState(fmt.Errorf("WARP underlay binding cannot be combined with detour"))
	}
	// Clear previous binding fields to avoid stale data.
	delete(endpoint, "detour")
	if dc.InterfaceName != "" {
		endpoint["bind_interface"] = dc.InterfaceName
	} else {
		delete(endpoint, "bind_interface")
	}
	if dc.SourceAddress != "" {
		addr, err := netip.ParseAddr(dc.SourceAddress)
		if err != nil {
			return fmt.Errorf("invalid source address %q", dc.SourceAddress)
		}
		if addr.Is4() {
			endpoint["inet4_bind_address"] = dc.SourceAddress
			delete(endpoint, "inet6_bind_address")
		} else {
			endpoint["inet6_bind_address"] = dc.SourceAddress
			delete(endpoint, "inet4_bind_address")
		}
		// When family is explicit, keep it; the endpoint's resolver already
		// handles the WARP inner traffic family separately.
	} else {
		delete(endpoint, "inet4_bind_address")
		delete(endpoint, "inet6_bind_address")
	}
	return nil
}

// ValidateDialConstraints checks a batch of constraints for global conflicts.
// Currently only validates each individually; server-ownership checks are done
// at deployment validation time where server inventory is available.
func ValidateDialConstraints(constraints map[int64]*NormalizedDialConstraint) error {
	for id, dc := range constraints {
		if dc == nil {
			continue
		}
		if dc.Mode == DialConstraintModeInterface && dc.InterfaceName == "" && dc.SourceAddress == "" {
			return markInvalidDesiredState(fmt.Errorf("warp profile %d: interface binding requires interface or address", id))
		}
	}
	return nil
}

// WARPUnderlayFromProfile extracts the underlay constraint from a WARPProfile's
// UnderlayJSON. Empty or missing JSON yields an auto constraint.
func WARPUnderlayFromProfile(profile model.WARPProfile) (*NormalizedDialConstraint, error) {
	raw := ""
	if profile.UnderlayJSON != "" {
		raw = profile.UnderlayJSON
	}
	// Back-compat: some callers store underlay in ConfigJSON's _underlay field;
	// prefer the dedicated column.
	return ValidateDialConstraint(raw)
}
