package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/auditrisk"
)

func TestAccountAuditResourceMeasurements(t *testing.T) {
	ctx := context.Background()
	s := activityPipelineStore(t, ":memory:")
	config, err := decodeAccountAuditPolicy("")
	if err != nil {
		t.Fatal(err)
	}
	config.Resources = AuditResourceThresholds{
		RequestRate: &AuditResourceThreshold{Start: 1, Full: 10, Unit: "requests/second"},
		Connections: &AuditResourceThreshold{Start: 0, Full: 100, Unit: "connections"},
		TrafficRate: &AuditResourceThreshold{Start: 0, Full: 1000, Unit: "bytes/second"},
	}
	invalid := config
	invalid.Resources.RequestRate = &AuditResourceThreshold{Start: 10, Full: 10, Unit: "requests/second"}
	if ValidateAccountAuditPolicy(invalid) == nil {
		t.Fatal("equal resource endpoints accepted")
	}
	for _, raw := range []string{`{"start":-1,"full":2}`, `{"start":0,"full":1.5}`, `{"start":0,"full":18446744073709551616}`} {
		var threshold AuditResourceThreshold
		if json.Unmarshal([]byte(raw), &threshold) == nil {
			t.Fatalf("invalid integer accepted: %s", raw)
		}
	}
	start := time.Unix(1800000000, 0).UTC().Truncate(time.Minute)
	asOf := start.Add(time.Minute)
	// Warmup rejects these requests, but resource accounting must count all of them.
	for i := 0; i < 120; i++ {
		if limited, _ := s.consumeAccountSubscriptionLimit(7, 60, start); !limited {
			t.Fatal("warmup unexpectedly admitted request")
		}
	}
	observed := s.AccountSubscriptionRequestRate(7, asOf)
	if !observed.Complete || observed.Requests != 120 || observed.Rate == nil || *observed.Rate != 2 || !observed.Start.Equal(start) || !observed.End.Equal(asOf) {
		t.Fatalf("raw requests: %+v", observed)
	}
	s.consumeAccountSubscriptionLimit(8, 60, start.Add(time.Second))
	if s.AccountSubscriptionRequestRate(8, asOf).Complete {
		t.Fatal("partial first minute certified")
	}
	if s.AccountSubscriptionRequestRate(9, asOf).Complete {
		t.Fatal("absent account certified")
	}
	yes := auditrisk.Dimension{State: auditrisk.Satisfied}
	quality := auditrisk.Quality{IdentityTrusted: yes, Deduplicated: yes, MeasurementValid: yes, CoverageComplete: yes, TimeAligned: yes, SourceSetComplete: yes, Freshness: yes, CapabilitySupported: yes}
	for _, stmt := range []string{
		`INSERT INTO account_activity_v1_expected VALUES(7,1,'kernel',?,1)`,
		`INSERT INTO account_activity_v1_coverage VALUES(1,'kernel',?,'v1',1,1)`,
		`INSERT INTO account_activity_v1_source VALUES(7,?,'v1','source',6000,7)`,
	} {
		if _, err := s.db.Exec(stmt, start.Unix()); err != nil {
			t.Fatal(err)
		}
	}
	dims, err := s.LoadAccountAuditResourceDimensions(ctx, 7, asOf, config, quality)
	if err != nil {
		t.Fatal(err)
	}
	if dims[0].Value == nil || *dims[0].Value != 120 || dims[0].Unit != "requests/minute" || *dims[0].Start != 60 || *dims[0].Full != 600 || dims[1].Value != nil || dims[2].Value == nil || *dims[2].Value != 6000 || dims[2].Unit != "bytes/minute" || *dims[2].Full != 60000 {
		t.Fatalf("measured dimensions: %+v", dims)
	}
	if _, err := s.db.Exec(`UPDATE account_activity_v1_coverage SET complete=0`); err != nil {
		t.Fatal(err)
	}
	dims, err = s.LoadAccountAuditResourceDimensions(ctx, 7, asOf, config, quality)
	if err != nil || dims[2].Value != nil {
		t.Fatalf("incomplete traffic treated as known: %+v %v", dims, err)
	}
	config.Resources = AuditResourceThresholds{}
	dims, err = s.LoadAccountAuditResourceDimensions(ctx, 7, asOf, config, quality)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range dims {
		if d.Value != nil || d.Start != nil || d.Full != nil {
			t.Fatalf("invented default: %+v", d)
		}
	}
	if got := s.AccountSubscriptionRequestRate(7, asOf.Add(time.Minute)); !got.Complete || got.Rate == nil || *got.Rate != 0 {
		t.Fatalf("retained idle minute: %+v", got)
	}
}
