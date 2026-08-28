package core

import (
	"fmt"

	"github.com/OboardProject/oboard/internal/model"
)

// SubscriptionRoute describes how a normalized proxy is emitted for a concrete
// client format. Templates must not choose this route.
type SubscriptionRoute string

const (
	SubscriptionRouteNative        SubscriptionRoute = "native"
	SubscriptionRouteMihomoBridge  SubscriptionRoute = "mihomo_bridge"
	SubscriptionRouteUnsupported   SubscriptionRoute = "unsupported"
)

// SubscriptionCapability is the single capability matrix consumed by filtering,
// rendering, preview, and tests. Protocol support results must match the
// previous subscriptionTargetSupports / surgeMacRoute behavior.
type SubscriptionCapability struct {
	Supported bool
	Route     SubscriptionRoute
}

func subscriptionCapability(format model.SubscriptionFormat, proxy subscriptionProxy, opts SubscriptionRenderOptions) SubscriptionCapability {
	format = normalizeSubscriptionFormat(format)
	if format == model.SubscriptionFormatAuto {
		return SubscriptionCapability{Route: SubscriptionRouteUnsupported}
	}
	if format == model.SubscriptionFormatSurgeMac {
		native, viaMihomo := surgeMacRoute(proxy, opts.SurgeMac)
		switch {
		case native:
			return SubscriptionCapability{Supported: true, Route: SubscriptionRouteNative}
		case viaMihomo:
			return SubscriptionCapability{Supported: true, Route: SubscriptionRouteMihomoBridge}
		default:
			return SubscriptionCapability{Route: SubscriptionRouteUnsupported}
		}
	}
	if subscriptionTargetSupports(format, proxy) {
		return SubscriptionCapability{Supported: true, Route: SubscriptionRouteNative}
	}
	return SubscriptionCapability{Route: SubscriptionRouteUnsupported}
}

func subscriptionTargetSupports(format model.SubscriptionFormat, proxy subscriptionProxy) bool {
	format = normalizeSubscriptionFormat(format)
	if format == model.SubscriptionFormatSurgeMac {
		return surgeMacSupports(proxy, defaultSurgeMacOptions())
	}
	if proxy.Type == "vmess" || proxy.Type == "trojan" || proxy.Type == "tuic" {
		switch format {
		case model.SubscriptionFormatSingBox, model.SubscriptionFormatSingBoxMieru,
			model.SubscriptionFormatMihomo,
			model.SubscriptionFormatStash, model.SubscriptionFormatShadowrocket,
			model.SubscriptionFormatV2Ray, model.SubscriptionFormatV2RayURI:
			return true
		default:
			return false
		}
	}
	if format == model.SubscriptionFormatSingBoxMieru {
		return proxy.Type != "ssh"
	}
	if proxy.Type == "mieru" {
		switch format {
		case model.SubscriptionFormatMihomo, model.SubscriptionFormatShadowrocket:
			return true
		default:
			return false
		}
	}
	if proxy.Type == "ssh" {
		switch format {
		case model.SubscriptionFormatSingBox, model.SubscriptionFormatMihomo, model.SubscriptionFormatShadowrocket, model.SubscriptionFormatStash, model.SubscriptionFormatEgern, model.SubscriptionFormatSurge, model.SubscriptionFormatSurgeMac, model.SubscriptionFormatV2Ray, model.SubscriptionFormatV2RayURI:
			return true
		default:
			return false
		}
	}
	if proxy.Type == "snell" {
		return snellFormatSupports(format, proxy)
	}
	switch format {
	case model.SubscriptionFormatSingBox, model.SubscriptionFormatMihomo, model.SubscriptionFormatShadowrocket:
		return true
	case model.SubscriptionFormatV2Ray, model.SubscriptionFormatV2RayURI:
		return proxy.Type != "ssh"
	case model.SubscriptionFormatStash:
		return stashSupports(proxy)
	case model.SubscriptionFormatEgern:
		return egernSupports(proxy)
	case model.SubscriptionFormatLoon:
		return loonSupports(proxy)
	case model.SubscriptionFormatQX:
		return qxSupports(proxy)
	case model.SubscriptionFormatSurge, model.SubscriptionFormatSurgeMac, model.SubscriptionFormatSurfboard:
		return surgeStyleSupports(format, proxy)
	default:
		return false
	}
}

func stashSupports(proxy subscriptionProxy) bool {
	if proxy.Type == "vless" && proxy.TLS.RealityPublicKey != "" && proxy.Network != "" && proxy.Network != "tcp" {
		return false
	}
	if proxy.Type == "anytls" {
		return (proxy.Network == "" || proxy.Network == "tcp") && proxy.TLS.RealityPublicKey == ""
	}
	if proxy.Type == "ss" {
		return stashCipher(proxy.Method)
	}
	return true
}

func egernSupports(proxy subscriptionProxy) bool {
	switch proxy.Type {
	case "vless":
		return stringSetContains([]string{"tcp", "ws", "http", "h2", "grpc"}, proxy.Network) && (proxy.Flow == "" || proxy.Flow == "xtls-rprx-vision")
	case "anytls":
		return proxy.Network == "" || proxy.Network == "tcp"
	case "ss":
		return stashCipher(proxy.Method)
	default:
		return true
	}
}

func loonSupports(proxy subscriptionProxy) bool {
	if proxy.Type == "vless" {
		return proxy.Network == "" || proxy.Network == "tcp" || proxy.Network == "ws" || proxy.Network == "http"
	}
	if proxy.Type == "anytls" {
		return proxy.Network == "" || proxy.Network == "tcp"
	}
	if proxy.Type == "ss" {
		return stashCipher(proxy.Method)
	}
	if proxy.Type == "hysteria2" && proxy.ObfsPassword != "" {
		return proxy.ObfsType == "salamander"
	}
	return true
}

func qxSupports(proxy subscriptionProxy) bool {
	switch proxy.Type {
	case "hysteria2":
		return false
	case "vless":
		return stringSetContains([]string{"tcp", "ws", "http"}, proxy.Network) && (proxy.Flow == "" || proxy.Flow == "xtls-rprx-vision")
	case "anytls":
		return proxy.Network == "" || proxy.Network == "tcp"
	case "ss":
		return stashCipher(proxy.Method)
	default:
		return true
	}
}

// snellFormatSupports mirrors the previous Sub-Store capability matrix for
// Snell nodes (checked against v4/v6 nodes OBoard emits):
//
//   - Surge and Surge Mac: native Snell v1-v6, both v4 and v6 nodes.
//   - Surfboard: Snell v1-v5 only, so v6 nodes are filtered.
//   - Mihomo: Snell v1-v5 only, v6 filtered.
//   - sing-box: v4 and v6 outbounds, both rendered.
//   - Stash: all Snell nodes are filtered.
//   - Egern: Snell v1-v5, v6 filtered.
//   - Shadowrocket: native snell:// share URIs support both v4 and v6.
//   - Loon, Quantumult X, and V2Ray URI formats: no Snell.
func snellFormatSupports(format model.SubscriptionFormat, proxy subscriptionProxy) bool {
	if proxy.Version != SnellVersionV4 && proxy.Version != SnellVersionV6 {
		return false
	}
	switch format {
	case model.SubscriptionFormatSurge, model.SubscriptionFormatSurgeMac:
		return true
	case model.SubscriptionFormatSurfboard:
		return proxy.Version == SnellVersionV4
	case model.SubscriptionFormatMihomo:
		return proxy.Version == SnellVersionV4
	case model.SubscriptionFormatSingBox, model.SubscriptionFormatSingBoxMieru:
		return true
	case model.SubscriptionFormatEgern:
		return proxy.Version == SnellVersionV4
	case model.SubscriptionFormatShadowrocket:
		return true
	case model.SubscriptionFormatLoon, model.SubscriptionFormatQX,
		model.SubscriptionFormatStash, model.SubscriptionFormatV2Ray, model.SubscriptionFormatV2RayURI:
		return false
	default:
		return false
	}
}

func surgeStyleSupports(format model.SubscriptionFormat, proxy subscriptionProxy) bool {
	switch proxy.Type {
	case "vless":
		return false
	case "anytls":
		return (proxy.Network == "" || proxy.Network == "tcp") && proxy.TLS.RealityPublicKey == ""
	case "ss":
		return stashCipher(proxy.Method)
	case "hysteria2":
		if proxy.ObfsType == "" {
			return true
		}
		if format == model.SubscriptionFormatSurfboard {
			return proxy.ObfsType == "salamander"
		}
		return proxy.ObfsType == "salamander" || proxy.ObfsType == "gecko"
	case "socks5":
		return true
	case "ssh":
		return format == model.SubscriptionFormatSurge || format == model.SubscriptionFormatSurgeMac
	default:
		return false
	}
}

func legacySSCipherSupported(method string) bool {
	return stringSetContains([]string{
		"aes-128-gcm", "aes-192-gcm", "aes-256-gcm", "aes-128-cfb", "aes-192-cfb", "aes-256-cfb",
		"aes-128-ctr", "aes-192-ctr", "aes-256-ctr", "rc4-md5", "chacha20-ietf", "xchacha20",
		"chacha20-ietf-poly1305", "xchacha20-ietf-poly1305",
	}, method)
}

func stashCipher(method string) bool {
	return legacySSCipherSupported(method) || stringSetContains([]string{"2022-blake3-aes-128-gcm", "2022-blake3-aes-256-gcm"}, method)
}

func filterCompatibleSubscriptionProxies(proxies []subscriptionProxy, format model.SubscriptionFormat, opts SubscriptionRenderOptions) []subscriptionProxy {
	compatible := make([]subscriptionProxy, 0, len(proxies))
	for _, proxy := range proxies {
		if subscriptionCapability(format, proxy, opts).Supported {
			compatible = append(compatible, proxy)
		}
	}
	return compatible
}

func assertConcreteSubscriptionFormat(format model.SubscriptionFormat) error {
	if normalizeSubscriptionFormat(format) == model.SubscriptionFormatAuto || !IsConcreteSubscriptionFormat(format) {
		return fmt.Errorf("subscription format %q is not a concrete renderer", format)
	}
	return nil
}
