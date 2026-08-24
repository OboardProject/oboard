package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/OboardProject/oboard/internal/model"
)

// Connection reuse and TCP Fast Open are three different capabilities, not one
// switch pair:
//
//   - GenericMux is the shared sing-box `multiplex` object (h2mux / smux /
//     yamux). Only VLESS and Shadowsocks accept it.
//   - NativeMux is the protocol's own reuse mechanism (AnyTLS sessions, QUIC
//     streams, Mieru multiplexing levels, Snell `reuse`, a WireGuard tunnel).
//     Those protocols must never also carry a generic `multiplex` object.
//   - TCPFastOpen is a shared listen/dial socket option. It is meaningful only
//     where the protocol's data path really runs over TCP, so Hysteria2
//     (QUIC/UDP) and WireGuard never expose it, and Mieru exposes it only in
//     TCP transport mode.
//
// Defaults stay off everywhere: TFO depends on the host `net.ipv4.tcp_fastopen`
// bitmask and on middlebox behaviour, and generic MUX changes client
// compatibility, so both are opt-in per inbound.
type GenericMuxSupport string

const (
	// GenericMuxNone marks a protocol that must never carry `multiplex`.
	GenericMuxNone GenericMuxSupport = "none"
	// GenericMuxSingBox marks a protocol accepting the sing-box multiplex object.
	GenericMuxSingBox GenericMuxSupport = "singbox"
)

type NativeMuxKind string

const (
	NativeMuxNone          NativeMuxKind = "none"
	NativeMuxAnyTLSSession NativeMuxKind = "anytls_session"
	NativeMuxQUICStream    NativeMuxKind = "quic"
	NativeMuxMieruLevel    NativeMuxKind = "mieru_level"
	NativeMuxSnellReuse    NativeMuxKind = "snell_reuse"
	NativeMuxSSH           NativeMuxKind = "ssh"
)

type TFOSupport string

const (
	// TFOUnsupported marks a data path that never runs over TCP.
	TFOUnsupported TFOSupport = "none"
	// TFOTCP marks a protocol whose data path is always TCP.
	TFOTCP TFOSupport = "tcp"
	// TFOTransportTCP marks a protocol whose data path is TCP only in some
	// transport modes (Mieru transport, VLESS v2ray transport).
	TFOTransportTCP TFOSupport = "transport_tcp"
)

// TransportCapability is the per-protocol reuse/TFO capability record the
// Controller, MCP catalog and Web UI all resolve their options from.
type TransportCapability struct {
	GenericMux GenericMuxSupport
	NativeMux  NativeMuxKind
	TFO        TFOSupport
}

// SupportsGenericMux reports whether the protocol accepts the sing-box
// `multiplex` object at all.
func (c TransportCapability) SupportsGenericMux() bool {
	return c.GenericMux == GenericMuxSingBox
}

// MayUseTCPFastOpen reports whether tcp_fast_open can ever apply, ignoring the
// concrete transport of a single inbound or outbound.
func (c TransportCapability) MayUseTCPFastOpen() bool {
	return c.TFO != TFOUnsupported
}

var protocolTransportCapabilities = map[model.Protocol]TransportCapability{
	model.ProtocolVLESS:  {GenericMux: GenericMuxSingBox, NativeMux: NativeMuxNone, TFO: TFOTransportTCP},
	model.ProtocolSS:     {GenericMux: GenericMuxSingBox, NativeMux: NativeMuxNone, TFO: TFOTCP},
	model.ProtocolAnyTLS: {GenericMux: GenericMuxNone, NativeMux: NativeMuxAnyTLSSession, TFO: TFOTCP},
	model.ProtocolHY2:    {GenericMux: GenericMuxNone, NativeMux: NativeMuxQUICStream, TFO: TFOUnsupported},
	model.ProtocolMieru:  {GenericMux: GenericMuxNone, NativeMux: NativeMuxMieruLevel, TFO: TFOTransportTCP},
	model.ProtocolSnell:  {GenericMux: GenericMuxNone, NativeMux: NativeMuxSnellReuse, TFO: TFOTCP},
	model.ProtocolSocks:  {GenericMux: GenericMuxNone, NativeMux: NativeMuxNone, TFO: TFOTCP},
	model.ProtocolSSH:    {GenericMux: GenericMuxNone, NativeMux: NativeMuxSSH, TFO: TFOUnsupported},
}

// ProtocolTransportCapability returns the capability record of a protocol.
// Unknown protocols get the most restrictive record so a new protocol never
// silently inherits generic MUX or TFO.
func ProtocolTransportCapability(protocol model.Protocol) TransportCapability {
	if capability, ok := protocolTransportCapabilities[protocol]; ok {
		return capability
	}
	return TransportCapability{GenericMux: GenericMuxNone, NativeMux: NativeMuxNone, TFO: TFOUnsupported}
}

// InboundSupportsTCPFastOpen reports whether the stored inbound really listens
// on TCP, so callers (UI, MCP guidance, validation) agree on one answer.
func InboundSupportsTCPFastOpen(inbound model.Inbound) bool {
	return protocolTCPDataPath(inbound.Protocol, parseExtra(inbound.ConfigJSON))
}

// OutboundSupportsTCPFastOpen mirrors InboundSupportsTCPFastOpen for the dial
// side of a managed or imported outbound.
func OutboundSupportsTCPFastOpen(protocol model.Protocol, configJSON string) bool {
	return protocolTCPDataPath(protocol, parseExtra(configJSON))
}

// protocolTCPDataPath decides whether a concrete configuration carries its
// payload over TCP. Only that case makes tcp_fast_open meaningful.
func protocolTCPDataPath(protocol model.Protocol, extra map[string]any) bool {
	switch ProtocolTransportCapability(protocol).TFO {
	case TFOTCP:
		return true
	case TFOTransportTCP:
		switch protocol {
		case model.ProtocolMieru:
			return normalizeMieruTransport(stringValue(extra, "transport", "TCP")) == "TCP"
		case model.ProtocolVLESS:
			return vlessTransportUsesTCP(extra)
		}
		return false
	default:
		return false
	}
}

// vlessTransportUsesTCP reports whether a VLESS v2ray transport keeps a TCP
// bottom layer. `quic` is the only supported transport that does not.
func vlessTransportUsesTCP(extra map[string]any) bool {
	transport, ok := extra["transport"].(map[string]any)
	if !ok {
		return true
	}
	return strings.ToLower(strings.TrimSpace(stringValue(transport, "type", ""))) != "quic"
}

// ValidateListenTransportConfig validates the connection-reuse and TCP Fast
// Open fields of a stored listen-side configuration document. Node presets and
// Snell parameter sets use it so a template can never carry a field the
// protocol would silently drop at deployment.
func ValidateListenTransportConfig(protocol model.Protocol, config map[string]any) error {
	return validateTransportOptions(protocol, config, transportSideInbound)
}

// transportSide distinguishes the sing-box listen side from the dial side:
// inbound multiplex accepts only enabled/padding, while the dial side also
// selects the mux protocol and stream limits.
type transportSide int

const (
	transportSideInbound transportSide = iota
	transportSideOutbound
)

var genericMuxProtocols = map[string]bool{"h2mux": true, "smux": true, "yamux": true}

// validateTransportOptions enforces the capability model on one config_json
// document. It fails loudly instead of letting the kernel silently ignore a
// field: sing-box drops `multiplex` on protocols that have no such option and
// ignores it whenever Shadowsocks `udp_over_tcp` is enabled.
func validateTransportOptions(protocol model.Protocol, extra map[string]any, side transportSide) error {
	capability := ProtocolTransportCapability(protocol)
	if raw, exists := extra["multiplex"]; exists && raw != nil {
		if !capability.SupportsGenericMux() {
			return fmt.Errorf("%s does not support the sing-box multiplex option (%s)", protocol, nativeMuxDescription(capability.NativeMux))
		}
		if err := validateGenericMuxOptions(raw, side); err != nil {
			return err
		}
	}
	if raw, exists := extra["tcp_fast_open"]; exists && raw != nil {
		enabled, ok := raw.(bool)
		if !ok {
			return errors.New("tcp_fast_open must be boolean")
		}
		if enabled && !protocolTCPDataPath(protocol, extra) {
			return fmt.Errorf("tcp_fast_open is not applicable to %s: %s", protocol, tfoRejectionReason(protocol, capability))
		}
	}
	return nil
}

func nativeMuxDescription(kind NativeMuxKind) string {
	switch kind {
	case NativeMuxAnyTLSSession:
		return "AnyTLS multiplexes streams over its own sessions"
	case NativeMuxQUICStream:
		return "Hysteria2 multiplexes streams over QUIC"
	case NativeMuxMieruLevel:
		return "Mieru uses its own multiplexing level"
	case NativeMuxSnellReuse:
		return "Snell uses its own connection reuse"
	case NativeMuxSSH:
		return "SSH multiplexes channels over its own transport"
	default:
		return "the protocol has no connection reuse layer"
	}
}

func tfoRejectionReason(protocol model.Protocol, capability TransportCapability) string {
	if capability.TFO == TFOUnsupported {
		if protocol == model.ProtocolHY2 {
			return "the Hysteria2 data path is QUIC over UDP"
		}
		return "the data path does not run over TCP"
	}
	if protocol == model.ProtocolMieru {
		return "UDP transport does not use a TCP socket"
	}
	return "the selected transport does not use a TCP socket"
}

// validateGenericMuxOptions keeps the generated object inside the fields
// OBoard exposes. TCP Brutal stays unexposed: h2mux with TCP Brutal is still
// reported upstream as able to retain unusable sessions.
func validateGenericMuxOptions(raw any, side transportSide) error {
	options, ok := raw.(map[string]any)
	if !ok {
		return errors.New("multiplex must be an object")
	}
	for key, value := range options {
		switch key {
		case "enabled", "padding":
			if _, ok := value.(bool); !ok && value != nil {
				return fmt.Errorf("multiplex.%s must be boolean", key)
			}
		case "protocol":
			if side != transportSideOutbound {
				return errors.New("multiplex.protocol is a client-side option and is not accepted on an inbound")
			}
			name, ok := value.(string)
			if !ok || !genericMuxProtocols[strings.ToLower(strings.TrimSpace(name))] {
				return errors.New("multiplex.protocol must be h2mux, smux or yamux")
			}
		case "max_connections", "min_streams", "max_streams":
			if side != transportSideOutbound {
				return fmt.Errorf("multiplex.%s is a client-side option and is not accepted on an inbound", key)
			}
			count, ok := exactJSONInt(value)
			if !ok || count < 0 {
				return fmt.Errorf("multiplex.%s must be a non-negative integer", key)
			}
		case "brutal":
			return errors.New("multiplex.brutal is not supported by OBoard")
		default:
			return fmt.Errorf("unsupported multiplex option %q", key)
		}
	}
	return nil
}

// exactJSONInt accepts only integral JSON numbers so a fractional stream limit
// is rejected instead of being truncated.
func exactJSONInt(value any) (int, bool) {
	switch typed := value.(type) {
	case float64:
		if typed != float64(int(typed)) {
			return 0, false
		}
		return int(typed), true
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			return 0, false
		}
		return int(parsed), true
	case int:
		return typed, true
	case int64:
		return int(typed), true
	default:
		return 0, false
	}
}

// genericMuxEnabled reports whether a stored config_json actually turns generic
// multiplexing on.
func genericMuxEnabled(value any) bool {
	options, ok := value.(map[string]any)
	if !ok {
		return false
	}
	enabled, _ := options["enabled"].(bool)
	return enabled
}
