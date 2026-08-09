package controller

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/model"
)

func TestResolveProxyPathRefRequiresPathAndEntryServerAccess(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	server := newTestServer(db, "test-secret", "")
	ctx := context.Background()

	node := &model.Server{Name: "restricted-entry", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 11000, Status: model.ServerOnline}
	if err := db.CreateServer(ctx, node); err != nil {
		t.Fatal(err)
	}
	inbound := &model.Inbound{ServerID: node.ID, Name: "restricted-inbound", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 10443, ConfigJSON: `{}`, Enabled: true}
	if err := db.CreateInbound(ctx, inbound); err != nil {
		t.Fatal(err)
	}
	path := &model.ProxyPath{Kind: model.ProxyPathKindChain, InboundID: inbound.ID, NameMode: model.ProxyPathNameAuto, ExitRegionMode: "auto", Secret: "test-secret", Enabled: true}
	if err := db.CreateProxyPath(ctx, path); err != nil {
		t.Fatal(err)
	}

	filter, err := json.Marshal(application.ResourceFilter{
		Servers:    &application.ResourceSelection{Mode: "none"},
		ProxyPaths: &application.ResourceSelection{Mode: "selected", IDs: []int64{path.ID}},
	})
	if err != nil {
		t.Fatal(err)
	}
	principal := application.Principal{ID: "path-only", ResourceFilter: filter}
	if _, err := server.resolveProxyPathRef(ctx, principal, "proxy_path:"+itoa(path.ID)); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("path without entry server access resolved with error %v", err)
	}

	filter, err = json.Marshal(application.ResourceFilter{
		Servers:    &application.ResourceSelection{Mode: "selected", IDs: []int64{node.ID}},
		ProxyPaths: &application.ResourceSelection{Mode: "selected", IDs: []int64{path.ID}},
	})
	if err != nil {
		t.Fatal(err)
	}
	principal.ResourceFilter = filter
	resolution, err := server.resolveProxyPathRef(ctx, principal, "proxy_path:"+itoa(path.ID))
	if err != nil || resolution.Value == nil || resolution.Value.ID != path.ID {
		t.Fatalf("authorized proxy path resolution = %#v, error = %v", resolution, err)
	}
}
