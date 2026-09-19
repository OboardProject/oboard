package controller

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

type huaweiDNSTransport func(*http.Request) (*http.Response, error)

func (f huaweiDNSTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestHuaweiDNSRequestSignature(t *testing.T) {
	payload := []byte(`{"name":"example.com."}`)
	req, err := http.NewRequest(http.MethodPost, "https://dns.cn-north-4.myhuaweicloud.com/v2/zones?tag=z&name=example.com.&tag=a+b", strings.NewReader(string(payload)))
	if err != nil {
		t.Fatal(err)
	}
	signHuaweiDNSRequest(req, payload, "test-ak", "test-sk", time.Date(2026, 9, 20, 1, 2, 3, 0, time.UTC))
	want := "SDK-HMAC-SHA256 Access=test-ak, SignedHeaders=host;x-sdk-date, Signature=fb03582579c3f770a084f72f7b555d247c4bb151f9a9940c7b8a05ad16db8466"
	if got := req.Header.Get("Authorization"); got != want {
		t.Fatalf("signature = %s", got)
	}
	if req.Header.Get("X-Sdk-Date") != "20260920T010203Z" || req.Header.Get("X-Auth-Token") != "" {
		t.Fatal("incorrect authentication headers")
	}
}

func TestHuaweiDNSAutomaticRegionAndWrite(t *testing.T) {
	for _, region := range huaweiDNSRegions {
		t.Run(region.id, func(t *testing.T) {
			for _, explicitID := range []string{"", "zone-1"} {
				reads, writes := 0, 0
				host := "dns." + region.id + ".myhuaweicloud.com"
				client := &http.Client{Transport: huaweiDNSTransport(func(r *http.Request) (*http.Response, error) {
					if r.URL.Scheme != "https" {
						t.Fatal("discovery must use HTTPS")
					}
					body := `{"zones":[]}`
					if r.Method == http.MethodGet {
						reads++
						if r.URL.Path != "/v2/zones" || r.URL.Query().Get("type") != "public" || r.URL.Query().Get("name") != "example.com." {
							t.Fatalf("unexpected discovery %s", r.URL)
						}
						if r.URL.Host == host {
							body = `{"zones":[{"id":"zone-1","name":"EXAMPLE.COM."}]}`
						}
					} else {
						writes++
						if r.Method != http.MethodPost || r.URL.Host != host || r.URL.Path != "/v2/zones/zone-1/recordsets" {
							t.Fatalf("write used wrong endpoint: %s %s", r.Method, r.URL)
						}
						body = `{"id":"record-1"}`
					}
					return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
				})}
				p := &huaweiDNSProvider{dnsProviderBase: dnsProviderBase{credential: model.DNSCredential{ZoneName: "example.com", ZoneID: explicitID}, httpClient: client}, accessKeyID: "test-ak", secretAccessKey: "test-sk"}
				if err := p.Verify(context.Background()); err != nil {
					t.Fatal(err)
				}
				before := reads
				if _, err := p.UpsertRecord(context.Background(), model.DNSRecord{Type: "A", Name: "www.example.com", Content: "192.0.2.1"}); err != nil {
					t.Fatal(err)
				}
				if reads != before || writes != 1 || p.region != region.id {
					t.Fatalf("reads %d -> %d, writes %d, region %s", before, reads, writes, p.region)
				}
			}
		})
	}
}

func TestHuaweiDNSDiscoveryFailure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"invalid credentials", 401, `{"message":"invalid access key"}`},
		{"no permissions", 403, `{"message":"forbidden"}`},
		{"wrong zone ID", 200, `{"zones":[{"id":"another-id","name":"example.com."}]}`},
		{"wrong domain", 200, `{"zones":[{"id":"zone-1","name":"other.example."}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			p := &huaweiDNSProvider{dnsProviderBase: dnsProviderBase{credential: model.DNSCredential{ZoneName: "example.com", ZoneID: "zone-1"}, httpClient: &http.Client{Transport: huaweiDNSTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != http.MethodGet {
					t.Fatal("discovery failure must not mutate DNS")
				}
				return &http.Response{StatusCode: tc.status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})}}, accessKeyID: "test-ak", secretAccessKey: "test-sk"}
			if err := p.Verify(context.Background()); err == nil || p.resolvedZoneID != "" || p.region != "" {
				t.Fatalf("unexpected verification success/state: %v", err)
			}
			if calls != len(huaweiDNSRegions) {
				t.Fatalf("tried %d endpoints", calls)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if err := p.Verify(ctx); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation: %v", err)
			}
			if calls != len(huaweiDNSRegions) {
				t.Fatal("cancelled discovery made requests")
			}
		})
	}
}

func TestHuaweiDNSConfigRequiresAccessKeys(t *testing.T) {
	if err := validateDNSProviderConfig(model.DNSProviderHuaweiCloud, map[string]string{"access_key_id": "ak", "secret_access_key": "sk"}); err != nil {
		t.Fatal(err)
	}
	for _, config := range []map[string]string{
		{"username": "user", "password": "password", "domain_name": "account", "region": "cn-north-4"},
		{"access_key_id": "ak"},
		{"secret_access_key": "sk"},
	} {
		if err := validateDNSProviderConfig(model.DNSProviderHuaweiCloud, config); err == nil {
			t.Fatal("incomplete AK/SK accepted")
		}
	}
}
