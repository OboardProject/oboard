package controller

import "testing"

func TestIsKnownUIPagePath(t *testing.T) {
	for _, path := range []string{"/", "/dashboard", "/dashboard/", "/servers", "/return-latency", "/return-latency/", "/proxy-paths", "/subscriptions", "/node-order-templates"} {
		if !isKnownUIPagePath(path) {
			t.Errorf("isKnownUIPagePath(%q) = false; want true", path)
		}
	}
	for _, path := range []string{"/not-a-real-page", "/login", "/admin", "/dashboard/extra", "/servers/1", "/api/v1/ui/version"} {
		if isKnownUIPagePath(path) {
			t.Errorf("isKnownUIPagePath(%q) = true; want false", path)
		}
	}
}
