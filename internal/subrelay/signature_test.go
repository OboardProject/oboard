package subrelay

import (
	"testing"
	"time"
)

func TestSignatureBindsRequestAndExpires(t *testing.T) {
	secret := "0123456789abcdef0123456789abcdef"
	now := time.Unix(1700000000, 0)
	ts := "1700000000"
	sig := Sign(secret, "GET", "/api/v1/subscriptions/token?format=mihomo", ts, "0123456789abcdef01234567", "203.0.113.8", "client", "etag")
	if err := Verify(secret, "GET", "/api/v1/subscriptions/token?format=mihomo", ts, "0123456789abcdef01234567", "203.0.113.8", "client", "etag", sig, now); err != nil {
		t.Fatal(err)
	}
	if err := Verify(secret, "GET", "/api/v1/subscriptions/other", ts, "0123456789abcdef01234567", "203.0.113.8", "client", "etag", sig, now); err == nil {
		t.Fatal("signature accepted for a different request")
	}
	if err := Verify(secret, "GET", "/api/v1/subscriptions/token?format=mihomo", ts, "0123456789abcdef01234567", "203.0.113.8", "client", "etag", sig, now.Add(MaxClockSkew+time.Second)); err == nil {
		t.Fatal("expired signature accepted")
	}
}
