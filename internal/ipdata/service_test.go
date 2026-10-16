package ipdata

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type fakeOffline struct{}

func (fakeOffline) Lookup(string) (model.IPGeography, error) {
	return model.IPGeography{CountryCode: "US", Country: "美国", Province: "加利福尼亚", City: "洛杉矶"}, nil
}

type fakeUpstream struct {
	mu     sync.Mutex
	calls  map[string]int
	status map[string]int
	bodies map[string]string
}

func (f *fakeUpstream) transport() http.RoundTripper {
	return roundTripFunc(func(req *http.Request) (*http.Response, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		host := req.URL.Host
		f.calls[host]++
		code := f.status[host]
		if code == 0 {
			code = 200
		}
		return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(f.bodies[host])), Header: http.Header{}}, nil
	})
}

func newFake() *fakeUpstream {
	return &fakeUpstream{calls: map[string]int{}, status: map[string]int{}, bodies: map[string]string{
		"api.ipinfo.io":     `{"ip":"1.1.1.1","asn":"AS13335","as_name":"Cloudflare, Inc.","country_code":"US","country":"United States"}`,
		"ipinfo.io":         `{"ip":"1.1.1.1","org":"AS13335 Cloudflare, Inc.","country":"US","region":"California","city":"Los Angeles"}`,
		"api.ipapi.is":      `{"ip":"1.1.1.1","is_proxy":false,"is_vpn":false,"is_tor":false,"is_datacenter":true,"is_abuser":false,"asn":{"asn":13335,"org":"Cloudflare"},"location":{"country_code":"US","state":"California","city":"San Jose"}}`,
		"ipwho.is":          `{"ip":"1.1.1.1","success":true,"country_code":"AU","region":"Queensland","city":"Brisbane","connection":{"asn":13335,"org":"APNIC"}}`,
		"proxycheck.io":     `{"status":"ok","1.1.1.1":{"network":{"asn":"AS13335","provider":"Cloudflare"},"location":{"country_code":"US"},"detections":{"proxy":false,"vpn":true,"tor":false,"hosting":true,"risk":33,"confidence":90}}}`,
		"api.abuseipdb.com": `{"data":{"ipAddress":"1.1.1.1","abuseConfidenceScore":0,"totalReports":2,"usageType":"Content Delivery Network"}}`,
	}}
}

func TestAttributionPrefersIPinfoAndReportsConflicts(t *testing.T) {
	fake := newFake()
	svc := New(func() Offline { return fakeOffline{} })
	svc.HTTP.Transport = fake.transport()
	svc.SetKeys(Keys{ProviderIPinfo: "token", ProviderAbuseIPDB: "abuse"})
	report := svc.Lookup(context.Background(), "1.1.1.1", Scope{Attribution: true, Quality: true})
	a := report.Attribution
	if a.Status != "ok" || a.Primary != ProviderIPinfo || a.ASN != "AS13335" || a.CountryCode != "US" || a.FieldSources["asn"] != ProviderIPinfo {
		t.Fatalf("attribution=%+v", a)
	}
	if a.City != "San Jose" || a.FieldSources["region"] != ProviderIPapiIs {
		t.Fatalf("city taken from a source disagreeing on country: %+v", a)
	}
	if a.CountryCheck.Agree != 4 || a.CountryCheck.Total != 5 || len(a.Conflicts) != 1 || a.Conflicts[0].Provider != ProviderIPWhois {
		t.Fatalf("agreement=%+v conflicts=%+v", a.CountryCheck, a.Conflicts)
	}
	q := report.Quality
	if q.Status != "ok" || q.Hosting.State != "yes" || q.VPN.State != "conflict" || q.Proxy.State != "no" || q.Risk == nil || *q.Risk != 33 || q.AbuseScore == nil || *q.AbuseScore != 0 {
		t.Fatalf("quality=%+v", q)
	}
	if q.VPN.Bool() != nil || q.Hosting.Bool() == nil || !*q.Hosting.Bool() {
		t.Fatal("verdict collapse wrong")
	}
	again := svc.Lookup(context.Background(), "1.1.1.1", Scope{Attribution: true, Quality: true})
	if again.Attribution.ASN != "AS13335" || fake.calls["api.ipinfo.io"] != 1 || fake.calls["api.ipapi.is"] != 1 {
		t.Fatalf("cache not reused: %+v", fake.calls)
	}
}

func TestAttributionDegradesWhenIPinfoFails(t *testing.T) {
	fake := newFake()
	fake.status["api.ipinfo.io"] = 401
	svc := New(func() Offline { return fakeOffline{} })
	svc.HTTP.Transport = fake.transport()
	svc.SetKeys(Keys{ProviderIPinfo: "bad"})
	a := svc.Lookup(context.Background(), "1.1.1.1", Scope{Attribution: true}).Attribution
	if a.Status != "degraded" || a.ASN != "AS13335" || a.CountryCode != "US" || a.Primary != ProviderIPapiIs {
		t.Fatalf("attribution=%+v", a)
	}
	if a.Sources[0].Status != StatusInvalidKey {
		t.Fatalf("ipinfo status=%s", a.Sources[0].Status)
	}
	svc.Lookup(context.Background(), "1.1.1.1", Scope{Attribution: true})
	if fake.calls["api.ipinfo.io"] != 1 {
		t.Fatal("invalid key was retried during cooldown")
	}
	fake.status["api.ipinfo.io"] = 200
	svc.SetKeys(Keys{ProviderIPinfo: "fixed"})
	if a = svc.Lookup(context.Background(), "1.1.1.1", Scope{Attribution: true}).Attribution; a.Status != "ok" {
		t.Fatalf("new key did not clear cooldown: %+v", a.Sources[0])
	}
}

func TestAttributionFallsBackToOfflineAndKeyless(t *testing.T) {
	fake := newFake()
	for _, host := range []string{"ipinfo.io", "api.ipapi.is", "ipwho.is"} {
		fake.status[host] = 500
	}
	svc := New(func() Offline { return fakeOffline{} })
	svc.HTTP.Transport = fake.transport()
	a := svc.Lookup(context.Background(), "1.1.1.1", Scope{Attribution: true}).Attribution
	if fake.calls["api.ipinfo.io"] != 0 || fake.calls["ipinfo.io"] != 1 {
		t.Fatalf("keyless endpoint not used: %+v", fake.calls)
	}
	if a.Status != "offline" || a.CountryCode != "US" || a.ASN != "" || a.City != "洛杉矶" {
		t.Fatalf("attribution=%+v", a)
	}
	q := svc.Lookup(context.Background(), "1.1.1.1", Scope{Quality: true}).Quality
	if q.Status != "partial" || q.Sources[2].Status != StatusNotConfigured {
		t.Fatalf("quality=%+v", q)
	}
}

func TestDecodersRejectMissingFields(t *testing.T) {
	var r Source
	if specs[ProviderProxycheck].decode("1.1.1.1", []byte(`{"status":"ok","1.1.1.1":{"network":{"asn":"AS13335"}}}`), false, &r) == nil {
		t.Fatal("missing detections accepted")
	}
	if specs[ProviderAbuseIPDB].decode("1.1.1.1", []byte(`{"data":{}}`), true, &r) == nil {
		t.Fatal("missing score accepted as zero")
	}
	if specs[ProviderIPinfo].decode("1.1.1.1", []byte(`{"ip":"8.8.8.8","asn":"AS15169","country_code":"US"}`), true, &r) == nil {
		t.Fatal("mismatched address accepted")
	}
	svc := New(nil)
	if svc.Lookup(context.Background(), "127.0.0.1", Scope{Attribution: true}).Attribution.Status != "unknown" {
		t.Fatal("private IP looked up")
	}
}

func TestPublishedKeylessBudgetsAndUncappedKeys(t *testing.T) {
	want := map[string]int{ProviderIPinfo: 1000, ProviderIPapiIs: 30, ProviderIPWhois: 1000, ProviderProxycheck: 100}
	for name, limit := range want {
		if got := specs[name].budget(false); got != limit {
			t.Fatalf("%s keyless budget=%d", name, got)
		}
		if got := specs[name].budget(true); got != 0 {
			t.Fatalf("%s keyed budget=%d", name, got)
		}
	}
	if specs[ProviderAbuseIPDB].budget(true) != 0 {
		t.Fatal("AbuseIPDB invented a plan quota")
	}
	fake := newFake()
	svc := New(nil)
	svc.HTTP.Transport = fake.transport()
	svc.SetKeys(Keys{ProviderIPWhois: "paid-plan"})
	svc.mu.Lock()
	svc.day = time.Now().UTC().Format("2006-01-02")
	svc.used[ProviderIPWhois] = 5000
	svc.used[ProviderIPapiIs] = 30
	svc.mu.Unlock()
	report := svc.Lookup(context.Background(), "1.1.1.1", Scope{Attribution: true})
	if fake.calls["ipwhois.pro"] != 1 {
		t.Fatalf("keyed lookup was capped locally: %+v", fake.calls)
	}
	var whois, ipapi Source
	for _, src := range report.Attribution.Sources {
		switch src.Provider {
		case ProviderIPWhois:
			whois = src
		case ProviderIPapiIs:
			ipapi = src
		}
	}
	if whois.Status == StatusQuotaExhausted {
		t.Fatalf("keyed provider quota=%+v", whois)
	}
	if ipapi.Status != StatusQuotaExhausted || fake.calls["api.ipapi.is"] != 0 {
		t.Fatalf("keyless cap not applied: %+v calls=%+v", ipapi, fake.calls)
	}
}

func TestIPapiIsDecodesKeyedAndAnonymousResponses(t *testing.T) {
	var keyed Source
	keyedBody := `{"ip":"1.1.1.1","is_proxy":false,"is_vpn":"NordVPN","is_tor":false,"is_datacenter":true,"is_abuser":false,"is_crawler":"GoogleBot","company":{"name":"APNIC"},"asn":{"asn":13335,"org":"Cloudflare, Inc."},"location":{"country_code":"au","country":"Australia","state":"Queensland","city":"Brisbane"}}`
	if err := specs[ProviderIPapiIs].decode("1.1.1.1", []byte(keyedBody), true, &keyed); err != nil {
		t.Fatal(err)
	}
	if keyed.ASN != "AS13335" || keyed.ASName != "Cloudflare, Inc." || keyed.Organization != "APNIC" || keyed.CountryCode != "AU" || keyed.City != "Brisbane" || keyed.VPN == nil || !*keyed.VPN || keyed.Proxy == nil || *keyed.Proxy || keyed.Hosting == nil || !*keyed.Hosting {
		t.Fatalf("keyed=%+v", keyed)
	}
	var anon Source
	anonBody := `{"ip":"1.1.1.1","is_bogon":false,"company":"APNIC Research and Development","asn":"AS13335 Cloudflare, Inc.","city":"Brisbane","region":"Queensland","country":"Australia","lat":-27.46,"lon":153.02,"timezone":"Australia/Brisbane","docs":"https://ipapi.is/free-tier.html?ref=api"}`
	if err := specs[ProviderIPapiIs].decode("1.1.1.1", []byte(anonBody), false, &anon); err != nil {
		t.Fatal(err)
	}
	if anon.ASN != "AS13335" || anon.ASName != "Cloudflare, Inc." || anon.Organization != "APNIC Research and Development" || anon.Country != "Australia" || anon.CountryCode != "" || anon.Region != "Queensland" || anon.City != "Brisbane" || anon.Proxy != nil || anon.VPN != nil || anon.Hosting != nil {
		t.Fatalf("anonymous=%+v", anon)
	}
	var flags Source
	if err := specs[ProviderIPapiIs].decode("1.1.1.1", []byte(`{"ip":"1.1.1.1","is_proxy":false,"is_datacenter":true,"company":null,"asn":null,"location":null}`), true, &flags); err != nil {
		t.Fatal(err)
	}
	if flags.ASN != "" || flags.Hosting == nil || !*flags.Hosting || flags.Proxy == nil || *flags.Proxy {
		t.Fatalf("flags=%+v", flags)
	}
	var quota Source
	if err := specs[ProviderIPapiIs].decode("1.1.1.1", []byte(`{"error":"quota exceeded","error_code":"ERR_QUOTA_EXCEEDED"}`), true, &quota); !errors.Is(err, errQuota) {
		t.Fatalf("quota err=%v", err)
	}
}

func TestIPapiIsTestSendsUnsavedKey(t *testing.T) {
	var bodies []string
	svc := New(nil)
	svc.HTTP.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != "api.ipapi.is" {
			return &http.Response{StatusCode: 500, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{}}, nil
		}
		raw, _ := io.ReadAll(req.Body)
		bodies = append(bodies, string(raw))
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ip":"1.1.1.1","is_proxy":false,"is_vpn":false,"is_tor":false,"is_datacenter":true,"is_abuser":false,"asn":{"asn":13335,"org":"Cloudflare, Inc."},"location":{"country_code":"AU","city":"Brisbane"}}`)), Header: http.Header{}}, nil
	})
	got := svc.Test(context.Background(), ProviderIPapiIs, "1.1.1.1", "test-key")
	if len(bodies) != 1 || !strings.Contains(bodies[0], `"key":"test-key"`) || got.Status != StatusOK || got.ASN != "AS13335" || got.CountryCode != "AU" || got.Hosting == nil || !*got.Hosting {
		t.Fatalf("bodies=%v got=%+v", bodies, got)
	}
	again := svc.Lookup(context.Background(), "1.1.1.1", Scope{Attribution: true})
	if len(bodies) < 2 || strings.Contains(bodies[len(bodies)-1], "test-key") || again.Attribution.Sources[1].Status != StatusOK {
		t.Fatalf("unsaved key leaked or was cached: %+v sources=%+v", bodies, again.Attribution.Sources)
	}
}
