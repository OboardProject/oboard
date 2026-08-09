package controller

import (
	"context"
	"testing"
)

func TestResolvePublicMetadataHostRejectsForbiddenAddresses(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "::1", "10.0.0.1", "169.254.169.254", "100.100.100.200"} {
		t.Run(host, func(t *testing.T) {
			if _, err := resolvePublicMetadataHost(context.Background(), host); err == nil {
				t.Fatalf("resolvePublicMetadataHost(%q) accepted a forbidden address", host)
			}
		})
	}
}

func TestDialPublicMetadataHostRejectsPrivateDestination(t *testing.T) {
	if _, err := dialPublicMetadataHost(context.Background(), "tcp", "127.0.0.1:443"); err == nil {
		t.Fatal("dialPublicMetadataHost accepted a private destination")
	}
}
