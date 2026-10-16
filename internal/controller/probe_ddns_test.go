package controller

import (
	"context"
	"fmt"
	"github.com/OboardProject/oboard/internal/model"
	"testing"
	"time"
)

type ddnsMemoryProvider struct {
	records map[string]model.DNSRecord
	next    int
}

func (p *ddnsMemoryProvider) Verify(context.Context) error { return nil }
func (p *ddnsMemoryProvider) ListRecords(context.Context) ([]model.DNSRecord, error) {
	var out []model.DNSRecord
	for _, r := range p.records {
		out = append(out, r)
	}
	return out, nil
}
func (p *ddnsMemoryProvider) UpsertRecord(_ context.Context, r model.DNSRecord) (model.DNSRecord, error) {
	if r.ID == "" {
		p.next++
		r.ID = fmt.Sprint(p.next)
	}
	p.records[r.ID] = r
	return r, nil
}
func (p *ddnsMemoryProvider) DeleteRecord(_ context.Context, id string) error {
	delete(p.records, id)
	return nil
}

func TestProbeDDNSOwnershipIsolation(t *testing.T) {
	ctx := context.Background()
	db := openControllerAutomationTestStore(t)
	srv := newTestServer(db, "test-secret", "")
	node := &model.Server{Name: "edge"}
	if err := db.CreateServer(ctx, node); err != nil {
		t.Fatal(err)
	}
	cred := &model.DNSCredential{Name: "cf", Provider: model.DNSProviderCloudflare, Enabled: true, ConfigEncrypted: "enc", Zones: []model.DNSCredentialZone{{ZoneName: "example.com", ProviderZoneID: "zone"}}}
	if err := db.CreateDNSCredential(ctx, cred); err != nil {
		t.Fatal(err)
	}
	provider := &ddnsMemoryProvider{records: map[string]model.DNSRecord{"manual": {ID: "manual", Name: "a.example.com", Type: "A", Content: "198.51.100.1"}}}
	a := model.ProbeDDNSRecord{ServerID: node.ID, Name: "carrier a", DNSCredentialID: &cred.ID, Domain: "a.example.com", RecordTypes: "a", IntervalSeconds: 300, Enabled: true}
	b := a
	b.Name = "carrier b"
	for _, r := range []*model.ProbeDDNSRecord{&a, &b} {
		if err := db.SaveProbeDDNS(ctx, *node, r); err != nil {
			t.Fatal(err)
		}
	}
	publish := func(r model.ProbeDDNSRecord, targets []dnsRecordTarget) {
		t.Helper()
		if _, err := srv.publishDNSTargets(ctx, provider, cred.ID, cred.Zones[0], r.Domain, false, r.Name, &node.ID, nil, targets, r.ID); err != nil {
			t.Fatal(err)
		}
	}
	publish(a, []dnsRecordTarget{{Type: "A", Content: "203.0.113.1"}})
	publish(b, []dnsRecordTarget{{Type: "A", Content: "203.0.113.2"}})
	a.Name = "renamed"
	publish(a, []dnsRecordTarget{{Type: "A", Content: "203.0.113.3"}})
	if len(provider.records) != 3 || provider.records["1"].Content != "203.0.113.3" || provider.records["2"].Content != "203.0.113.2" {
		t.Fatalf("cross-task update: %+v", provider.records)
	}
	publish(a, []dnsRecordTarget{{Type: "AAAA", Content: "2001:db8::1"}})
	if len(provider.records) != 3 || provider.records["2"].Content != "203.0.113.2" {
		t.Fatalf("type change touched peer: %+v", provider.records)
	}
	publish(a, nil)
	if len(provider.records) != 2 || provider.records["manual"].Content != "198.51.100.1" || provider.records["2"].Content != "203.0.113.2" {
		t.Fatalf("cross-task deletion: %+v", provider.records)
	}
}

func TestProbeDDNSRefreshInventory(t *testing.T) {
	ctx := context.Background()
	db := openControllerAutomationTestStore(t)
	srv := newTestServer(db, "test-secret", "")
	node := &model.Server{Name: "edge", AgentID: "edge-agent", Status: model.ServerOnline}
	if err := db.CreateServer(ctx, node); err != nil {
		t.Fatal(err)
	}
	task := &model.AgentTask{ServerID: node.ID, Type: model.AgentTaskTypeListNetworkInterfaces, PayloadJSON: "{}", ResultJSON: "{}", Status: "pending", Nonce: "refresh"}
	if err := db.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		time.Sleep(20 * time.Millisecond)
		done <- db.CompleteTask(ctx, task.ID, "succeeded", `{"interfaces":[{"name":"eth0","up":true,"running":true,"addresses":["203.0.113.9/24"]}]}`)
	}()
	if err := srv.refreshProbeDDNSInventory(ctx, *node); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := srv.refreshProbeDDNSInventory(ctx, *node); err != nil {
		t.Fatal(err)
	}
	tasks, err := db.ListTasksByServer(ctx, node.ID, 100)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("did not reuse pending/fresh inventory: %+v %v", tasks, err)
	}
	node.Status = model.ServerOffline
	if err := srv.refreshProbeDDNSInventory(ctx, *node); err == nil {
		t.Fatal("offline inventory published")
	}
}
