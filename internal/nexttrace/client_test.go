package nexttrace

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func TestFillQueriesOnlyUnattributedPublicHopsWithV4Token(t *testing.T) {
	var queried atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v4/ipGeo" || r.Header.Get("X-NextTrace-Token") != "secret" {
			http.Error(w, "denied", 403)
			return
		}
		queried.Add(1)
		ip := r.URL.Query().Get("ip")
		if ip != "8.8.8.8" {
			t.Errorf("queried %s", ip)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ip": ip, "asnumber": "15169", "owner": "Google", "country": "美国"})
	}))
	defer server.Close()
	client := New(time.Now, "")
	client.SetToken("secret")
	client.endpoint = server.URL
	trace := model.NetworkTraceResult{Hops: []model.NetworkTraceHop{
		{Hop: 1, Addresses: []string{"10.0.0.1"}},
		{Hop: 2, Addresses: []string{"1.1.1.1"}, Geography: []model.NetworkTraceGeography{{Address: "1.1.1.1", ASN: "AS13335"}}},
		{Hop: 3, Addresses: []string{"8.8.8.8"}},
	}}
	if err := client.Fill(t.Context(), &trace); err != nil {
		t.Fatal(err)
	}
	got := trace.Hops[2].Geography
	if queried.Load() != 1 || len(got) != 1 || got[0].ASN != "AS15169" || got[0].Source != SourceController || len(trace.Hops[1].Geography) != 1 {
		t.Fatalf("queried=%d hops=%+v", queried.Load(), trace.Hops)
	}
	trace.Hops[2].Geography = nil
	if err := client.Fill(t.Context(), &trace); err != nil || queried.Load() != 1 || !reflect.DeepEqual(trace.Hops[2].Geography, got) {
		t.Fatalf("cache not reused: queried=%d", queried.Load())
	}
}

func TestNextTraceChallengeBounds(t *testing.T) {
	for raw, want := range map[string][]string{"35": {"5", "7"}, "10403": {"101", "103"}} {
		if got, err := solveChallenge(t.Context(), raw); err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("%s got=%v err=%v", raw, got, err)
		}
	}
	for _, raw := range []string{"0", "13", "-1"} {
		if _, err := solveChallenge(t.Context(), raw); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}
