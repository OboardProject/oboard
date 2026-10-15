package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func TestProbeDDNSMigratesPreviousInbound(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ddns.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	server := &model.Server{Name: "edge", PublicIPv4: "203.0.113.10", PublicIPv6: "2001:db8::10"}
	if err = db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	verified := time.Now().UTC()
	credential := &model.DNSCredential{Name: "cf", Provider: model.DNSProviderCloudflare, Enabled: true, VerifiedAt: &verified, ConfigEncrypted: "enc", Zones: []model.DNSCredentialZone{{ZoneName: "example.com", ProviderZoneID: "zone"}}}
	if err = db.CreateDNSCredential(ctx, credential); err != nil {
		t.Fatal(err)
	}
	otherCredential := &model.DNSCredential{Name: "other", Provider: model.DNSProviderCloudflare, Enabled: true, VerifiedAt: &verified, ConfigEncrypted: "enc", Zones: []model.DNSCredentialZone{{ZoneName: "example.net", ProviderZoneID: "zone2"}}}
	if err = db.CreateDNSCredential(ctx, otherCredential); err != nil {
		t.Fatal(err)
	}
	sharedA := &model.Inbound{ServerID: server.ID, Name: "入口 A", Protocol: model.ProtocolSS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: "{}", Enabled: true, DNSSyncEnabled: true, DNSCredentialID: &credential.ID, DNSDomain: "Edge.Example.com", DNSRecordTypes: "a", EntryIPMode: model.EntryIPModeAuto}
	sharedB := &model.Inbound{ServerID: server.ID, Name: "入口 B", Protocol: model.ProtocolSS, ListenIP: "0.0.0.0", Port: 444, ConfigJSON: "{}", Enabled: false, DNSSyncEnabled: true, DNSCredentialID: &credential.ID, DNSDomain: "edge.example.com.", DNSRecordTypes: "aaaa", DNSProxyEnabled: true, EntryIPMode: model.EntryIPModeIPv6}
	plain := &model.Inbound{ServerID: server.ID, Name: "静态", Protocol: model.ProtocolSS, ListenIP: "0.0.0.0", Port: 445, ConfigJSON: "{}", Enabled: true}
	broken := &model.Inbound{ServerID: server.ID, Name: "缺域名", Protocol: model.ProtocolSS, ListenIP: "0.0.0.0", Port: 446, ConfigJSON: "{}", Enabled: true, DNSCredentialID: &otherCredential.ID}
	for _, inbound := range []*model.Inbound{sharedA, sharedB, plain, broken} {
		if err = db.CreateInbound(ctx, inbound); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.db.Exec(`update inbounds set ddns_enabled=1, ddns_interval_seconds=900 where id in (?,?)`, sharedA.ID, sharedB.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.db.Exec(`update inbounds set ddns_enabled=1, ddns_interval_seconds=300, dns_domain='' where id=?`, broken.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.db.Exec(`drop trigger if exists config_rev_inbounds_update`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.db.Exec(`alter table inbounds drop column ddns_record_id`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.db.Exec(`drop table probe_ddns_records`); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	records, err := db.ListProbeDDNS(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("records=%d", len(records))
	}
	record := records[0]
	if record.ServerID != server.ID || record.Domain != "edge.example.com" || record.RecordTypes != "both" || !record.ProxyEnabled || record.IntervalSeconds != 900 || !record.Enabled {
		t.Fatalf("record=%+v", record)
	}
	if record.DNSCredentialID == nil || *record.DNSCredentialID != credential.ID || record.InterfaceAddress != "" {
		t.Fatalf("binding=%+v", record)
	}
	inbounds, err := db.ListInbounds(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[int64]model.Inbound{}
	for _, inbound := range inbounds {
		byID[inbound.ID] = inbound
	}
	for _, id := range []int64{sharedA.ID, sharedB.ID} {
		got := byID[id]
		if got.DDNSRecordID == nil || *got.DDNSRecordID != record.ID || got.DDNSDomain != record.Domain {
			t.Fatalf("inbound %d not linked: %+v", id, got)
		}
	}
	if byID[plain.ID].DDNSRecordID != nil || byID[broken.ID].DDNSRecordID != nil {
		t.Fatalf("unrelated inbounds linked: %+v %+v", byID[plain.ID], byID[broken.ID])
	}
	var stillOn int
	if err = db.db.QueryRow(`select count(*) from inbounds where ddns_enabled=1`).Scan(&stillOn); err != nil {
		t.Fatal(err)
	}
	if stillOn != 0 {
		t.Fatalf("old DDNS flag still set: %d", stillOn)
	}
	if current, err := db.GetServer(ctx, server.ID); err != nil || current.Name != server.Name {
		t.Fatalf("server changed: %+v %v", current, err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	again, err := db.ListProbeDDNS(ctx)
	if err != nil || len(again) != 1 || again[0].ID != record.ID || again[0].Revision != record.Revision {
		t.Fatalf("reopen=%+v %v", again, err)
	}
}
