package controller

import "strings"

// knownUIPagePaths is the exact set of panel routes that may fall back to
// index.html. Unknown paths must 404 instead of serving the login SPA.
// Keep in sync with web/src/main.tsx pathTabs.
var knownUIPagePaths = map[string]struct{}{
	"/":                     {},
	"/account":              {},
	"/audit":                {},
	"/automation":           {},
	"/dashboard":            {},
	"/dns":                  {},
	"/dns-records":          {},
	"/external-outbounds":   {},
	"/inbounds":             {},
	"/mtu":                  {},
	"/node-order-templates": {},
	"/nodes":                {},
	"/notifications":        {},
	"/outbounds":            {},
	"/plans":                {},
	"/port-forwards":        {},
	"/proxy-paths":          {},
	"/return-latency":       {},
	"/routing":              {},
	"/servers":              {},
	"/settings":             {},
	"/subscriptions":        {},
	"/tasks":                {},
	"/tunnels":              {},
	"/users":                {},
}

func isKnownUIPagePath(path string) bool {
	cleaned := strings.TrimRight(path, "/")
	if cleaned == "" {
		cleaned = "/"
	}
	_, ok := knownUIPagePaths[cleaned]
	return ok
}
