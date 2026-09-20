package store

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

func BenchmarkAccountAuditSnapshotPage(b *testing.B) {
	s, err := Open(filepath.Join(b.TempDir(), "audit.sqlite"))
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	for i := 0; i < 50; i++ {
		u := &model.User{Username: fmt.Sprintf("audit-%d", i), Role: model.RoleViewer, Status: "active"}
		if err := s.CreateUser(ctx, u); err != nil {
			b.Fatal(err)
		}
		if err := s.SaveAccountAuditSnapshot(ctx, accountAuditSnapshot(u.ID, 1, 80), 1); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.ListAccountAuditSnapshots(ctx, AccountAuditQuery{Limit: 50}); err != nil {
			b.Fatal(err)
		}
	}
}
