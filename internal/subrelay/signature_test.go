package subrelay

import (
	"testing"
	"time"
)

func TestSignatureBindsRequestAndExpires(t *testing.T) {
	secret := "0123456789abcdef0123456789abcdef"
	now := time.Unix(1700000000, 0)
	ts := "1700000000"
	sig := Sign(secret, "42", "GET", "/api/v1/subscriptions/token?format=mihomo", ts, "0123456789abcdef01234567", "203.0.113.8", "client", "etag")
	if err := Verify(secret, "42", "GET", "/api/v1/subscriptions/token?format=mihomo", ts, "0123456789abcdef01234567", "203.0.113.8", "client", "etag", sig, now); err != nil {
		t.Fatal(err)
	}
	if err := Verify(secret, "42", "GET", "/api/v1/subscriptions/other", ts, "0123456789abcdef01234567", "203.0.113.8", "client", "etag", sig, now); err == nil {
		t.Fatal("signature accepted for a different request")
	}
	if err := Verify(secret, "43", "GET", "/api/v1/subscriptions/token?format=mihomo", ts, "0123456789abcdef01234567", "203.0.113.8", "client", "etag", sig, now); err == nil {
		t.Fatal("signature accepted for a different relay")
	}
	if err := Verify(secret, "42", "GET", "/api/v1/subscriptions/token?format=mihomo", ts, "0123456789abcdef01234567", "203.0.113.8", "client", "etag", sig, now.Add(MaxClockSkew+time.Second)); err == nil {
		t.Fatal("expired signature accepted")
	}
}

func TestControlSignatureBindsBodyAndRelay(t *testing.T) {
	secret := "0123456789abcdef0123456789abcdef"
	now := time.Unix(1700000000, 0)
	body := []byte(`{"build":"123"}`)
	signature := SignControl(secret, "42", "POST", "/api/v1/subscription-relay/heartbeat", "1700000000", "0123456789abcdef01234567", body)
	if err := VerifyControl(secret, "42", "POST", "/api/v1/subscription-relay/heartbeat", "1700000000", "0123456789abcdef01234567", body, signature, now); err != nil {
		t.Fatal(err)
	}
	if err := VerifyControl(secret, "42", "POST", "/api/v1/subscription-relay/heartbeat", "1700000000", "0123456789abcdef01234567", []byte(`{"build":"old"}`), signature, now); err == nil {
		t.Fatal("control signature accepted a different body")
	}
}
