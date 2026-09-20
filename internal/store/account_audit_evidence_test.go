package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func TestAccountAuditEvidenceBoundedAuthorizedStoredWindow(t *testing.T) {
	s, user := accountAuditFixture(t)
	ctx := context.Background()
	snapshot := accountAuditSnapshot(user, 30, 80)
	raw, _ := json.Marshal(snapshot)
	result, err := s.db.Exec(`INSERT INTO account_audit_events(user_id,risk_type,cycle,status,score,first_seen_at,last_seen_at,snapshot) VALUES(?,'activity',1,'pending',80,?,?,?)`, user, snapshot.AsOf.Format(time.RFC3339Nano), snapshot.AsOf.Format(time.RFC3339Nano), string(raw))
	if err != nil {
		t.Fatal(err)
	}
	event, _ := result.LastInsertId()
	server := &model.Server{Name: "evidence-node", ListenIP: "0.0.0.0", Status: model.ServerOnline}
	if err = s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	insert := func(id string, at time.Time) {
		t.Helper()
		stamp := at.Format(time.RFC3339Nano)
		_, err := s.db.Exec(`INSERT INTO connection_audit_reports(report_id,server_id,user_id,source_ip,device_id_hash,network,destination,connection_count,upload_bytes,download_bytes,collection_started_at,collection_ended_at,started_at,ended_at,created_at) VALUES(?,?,?,'1.2.3.4','secret-device','tcp','https://secret.example/token',1,12,34,?,?,?,?,?)`, id, server.ID, user, stamp, stamp, stamp, stamp, stamp)
		if err != nil {
			t.Fatal(err)
		}
	}
	insert("a", snapshot.WindowStart.Add(time.Minute))
	insert("b", snapshot.WindowStart.Add(time.Minute))
	insert("old", snapshot.WindowStart.Add(-time.Minute))
	insert("future", snapshot.WindowEnd.Add(time.Minute))
	q := AccountAuditEvidenceQuery{EventID: event, Limit: 1}
	allow := func(id int64) bool { return id == user }
	if _, err = s.AccountAuditEvidence(ctx, q, func(int64) bool { return false }); err == nil {
		t.Fatal("scope bypass")
	}
	page, err := s.AccountAuditEvidence(ctx, q, allow)
	if err != nil || len(page.Items) != 1 || page.NextOffset == nil || page.Status != "partial" || page.Items[0].UploadBytes != 12 {
		t.Fatalf("page: %+v %v", page, err)
	}
	q.Offset = *page.NextOffset
	page, err = s.AccountAuditEvidence(ctx, q, allow)
	if err != nil || len(page.Items) != 1 || page.NextOffset != nil {
		t.Fatalf("pagination/window: %+v %v", page, err)
	}
	encoded, _ := json.Marshal(page)
	for _, forbidden := range []string{"secret", "1.2.3.4", "device", "destination", "token"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatal("sensitive evidence leaked")
		}
	}
	q.Limit = 101
	if _, err = s.AccountAuditEvidence(ctx, q, allow); err == nil {
		t.Fatal("unbounded limit")
	}
	if _, err = s.db.Exec(`DELETE FROM connection_audit_reports`); err != nil {
		t.Fatal(err)
	}
	q.Limit = 20
	q.Offset = 0
	page, err = s.AccountAuditEvidence(ctx, q, allow)
	if err != nil || len(page.Items) != 0 || page.Status != "unknown" || page.Reason == "" {
		t.Fatalf("absence claimed complete: %+v %v", page, err)
	}
}
