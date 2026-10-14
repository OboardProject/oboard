package ipquality

import (
	"context"
	"errors"
	"github.com/OboardProject/oboard/internal/model"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestASNPathConservativeComparison(t *testing.T) {
	trace := model.NetworkTraceResult{Reached: true, Hops: []model.NetworkTraceHop{{Hop: 1, Addresses: []string{"10.0.0.1"}}, {Hop: 2, Addresses: []string{"1.1.1.1"}}, {Hop: 3, Addresses: []string{"1.0.0.1"}}, {Hop: 4, Addresses: []string{"8.8.8.8"}}}}
	lookup := func(ip string) (string, error) {
		if ip == "8.8.8.8" {
			return "AS15169", nil
		}
		return "AS13335", nil
	}
	got, err := ASNPath(trace, lookup)
	if err != nil || !reflect.DeepEqual(got, []string{"AS13335", "AS15169"}) {
		t.Fatalf("path=%v err=%v", got, err)
	}
	for _, change := range []func(*model.NetworkTraceResult){func(v *model.NetworkTraceResult) { v.Reached = false }, func(v *model.NetworkTraceResult) { v.Truncated = true }, func(v *model.NetworkTraceResult) { v.Hops[1].Addresses = nil }, func(v *model.NetworkTraceResult) { v.Hops[1].Addresses = []string{"1.1.1.1", "8.8.8.8"} }, func(v *model.NetworkTraceResult) { v.Hops[1].Hop = 3 }} {
		copy := trace
		copy.Hops = append([]model.NetworkTraceHop{}, trace.Hops...)
		change(&copy)
		if _, err := ASNPath(copy, lookup); err == nil {
			t.Fatal("incomplete or ambiguous path accepted")
		}
	}
	if _, err := ASNPath(trace, func(string) (string, error) { return "", errors.New("quota") }); err == nil {
		t.Fatal("failed ASN lookup accepted")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestIPQualityMissingFieldsAndCache(t *testing.T) {
	var r Result
	if decode("proxycheck", "1.1.1.1", []byte(`{"status":"ok","1.1.1.1":{"network":{"asn":"AS13335"}}}`), &r) == nil {
		t.Fatal("missing detections accepted")
	}
	if decode("abuseipdb", "1.1.1.1", []byte(`{"data":{}}`), &r) == nil {
		t.Fatal("missing score accepted as zero")
	}
	body := `{"status":"ok","1.1.1.1":{"detections":{"proxy":false,"vpn":false,"hosting":true,"risk":33,"confidence":100}}}`
	client := New(Config{})
	calls := 0
	client.HTTP.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
	})
	first := client.Lookup(context.Background(), "proxycheck", "1.1.1.1")
	second := client.Lookup(context.Background(), "proxycheck", "1.1.1.1")
	if first.Status != "ok" || second.Status != "ok" || calls != 1 || first.Proxy == nil || *first.Proxy || first.Risk == nil || *first.Risk != 33 {
		t.Fatalf("result=%+v calls=%d", first, calls)
	}
	if client.Lookup(context.Background(), "ipinfo", "1.1.1.1").Status != "not_configured" {
		t.Fatal("missing token accepted")
	}
	if client.Lookup(context.Background(), "proxycheck", "127.0.0.1").Status == "ok" || calls != 1 {
		t.Fatal("private IP queried")
	}
}
