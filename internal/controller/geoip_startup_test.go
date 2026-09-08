package controller

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

type blockedHistoryGeoResolver struct {
	started chan struct{}
	release chan struct{}
}

func (g *blockedHistoryGeoResolver) Lookup(string) (model.IPGeography, error) {
	close(g.started)
	<-g.release
	return model.IPGeography{CountryCode: "US", Revision: "new-geo"}, nil
}
func (g *blockedHistoryGeoResolver) Status() model.GeoDatabaseStatus {
	return model.GeoDatabaseStatus{Available: true, Revision: "new-geo"}
}
func (g *blockedHistoryGeoResolver) Close() {}

func TestHealthAvailableWhileGeoHistoryRefreshIsPending(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "controller.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	node := &model.Server{Name: "geo-node", Status: model.ServerOnline}
	if err := db.CreateServer(t.Context(), node); err != nil {
		t.Fatal(err)
	}
	user := &model.User{Username: "geo-user", PasswordHash: "unused", Role: model.RoleViewer, Status: "active", ProxyUUID: "geo-user-uuid", ProxyPassword: "geo-user-password"}
	if err := db.CreateUser(t.Context(), user); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AddConnectionAuditReportsResult(t.Context(), []model.ConnectionAuditReport{{
		ReportID: "geo-report", ServerID: node.ID, UserID: user.ID, SourceIP: "1.1.1.1", GeoDatabaseRevision: "old-geo",
	}}); err != nil {
		t.Fatal(err)
	}
	app := newTestServer(db, "test-secret", "")
	resolver := &blockedHistoryGeoResolver{started: make(chan struct{}), release: make(chan struct{})}
	app.geoIP, app.geoIPStatus = resolver, resolver.Status()
	done := make(chan struct{})
	go func() {
		defer close(done)
		app.RefreshGeoIPHistory(t.Context())
	}()
	defer func() {
		close(resolver.release)
		<-done
		remaining, err := db.ConnectionAuditSourceIPsForGeoRevision(t.Context(), "new-geo")
		if err != nil || len(remaining) != 0 {
			t.Errorf("history refresh did not finish: remaining=%v err=%v", remaining, err)
		}
	}()
	select {
	case <-resolver.started:
	case <-time.After(5 * time.Second):
		t.Fatal("history refresh did not start")
	}
	server := httptest.NewServer(app.Handler())
	defer server.Close()
	client := &http.Client{Timeout: time.Second}
	response, err := client.Get(server.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("health while history refresh is blocked = %d", response.StatusCode)
	}
}
