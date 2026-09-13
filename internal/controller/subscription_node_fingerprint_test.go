package controller

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

// TestPublishedSubscriptionTracksItsOwnInputs is the safety property behind
// per-node publication keys: every row a node is rendered from has to move its
// fingerprint, and a change to a node this user does not have must not.
//
// Getting this wrong is the worst failure this panel can have - a client keeps
// receiving the previous credentials or the previous address - so each relation
// is asserted rather than assumed.
func TestPublishedSubscriptionTracksItsOwnInputs(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "fingerprint.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	newServer := func(name string) *model.Server {
		server := &model.Server{Name: name, EntryAddress: "203.0.113.1", Status: model.ServerOnline, ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 20000}
		if err := db.CreateServer(ctx, server); err != nil {
			t.Fatal(err)
		}
		return server
	}
	entry, hop, other := newServer("entry"), newServer("hop"), newServer("other")
	newInbound := func(server *model.Server, name string, port int) *model.Inbound {
		inbound := &model.Inbound{ServerID: server.ID, Name: name, Protocol: model.ProtocolVLESS, Port: port, ListenIP: "0.0.0.0", Enabled: true}
		if err := db.CreateInbound(ctx, inbound); err != nil {
			t.Fatal(err)
		}
		return inbound
	}
	entryInbound, hopInbound, otherInbound := newInbound(entry, "entry-in", 443), newInbound(hop, "hop-in", 444), newInbound(other, "other-in", 445)
	path := &model.ProxyPath{InboundID: entryInbound.ID, Kind: model.ProxyPathKindChain, Enabled: true}
	if err := db.CreateProxyPath(ctx, path); err != nil {
		t.Fatal(err)
	}
	step := &model.ProxyPathStep{PathID: path.ID, Position: 0, NodeType: model.ProxyPathStepServerInbound, TransportMode: model.ProxyPathTransportSingBox, ServerID: &hop.ID, InboundID: &hopInbound.ID}
	if err := db.CreateProxyPathStep(ctx, step); err != nil {
		t.Fatal(err)
	}
	nodes := map[string]bool{core.NodeKeyOf(model.AssignableNodeProxyPath, path.ID): true}

	fingerprint := func() string {
		t.Helper()
		data, err := db.FullRoutingConfigData(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return subscriptionNodeFingerprintsFor(buildSubscriptionNodeFingerprints(data), nodes)[core.NodeKeyOf(model.AssignableNodeProxyPath, path.ID)]
	}

	previous := fingerprint()
	if previous == "" {
		t.Fatal("the path has no fingerprint")
	}
	mustChange := func(what string, mutate func()) {
		t.Helper()
		mutate()
		current := fingerprint()
		if current == previous {
			t.Fatalf("%s did not change what the published subscription is keyed on", what)
		}
		previous = current
	}

	mustChange("renaming the entry inbound", func() {
		entryInbound.Name = "entry-renamed"
		if err := db.UpdateInbound(ctx, entryInbound); err != nil {
			t.Fatal(err)
		}
	})
	mustChange("changing the entry server address", func() {
		entry.EntryAddress = "203.0.113.9"
		if err := db.UpdateServer(ctx, entry); err != nil {
			t.Fatal(err)
		}
	})
	mustChange("changing the hop inbound port", func() {
		hopInbound.Port = 1444
		if err := db.UpdateInbound(ctx, hopInbound); err != nil {
			t.Fatal(err)
		}
	})
	mustChange("changing the hop server", func() {
		hop.EntryAddress = "203.0.113.8"
		if err := db.UpdateServer(ctx, hop); err != nil {
			t.Fatal(err)
		}
	})
	mustChange("renaming the path", func() {
		path.NameMode = model.ProxyPathNameCustom
		path.NameTemplate = []model.ProxyPathNamePart{{Kind: model.ProxyPathNameServer, ServerID: hop.ID}}
		if err := db.UpdateProxyPath(ctx, path); err != nil {
			t.Fatal(err)
		}
	})
	mustChange("allocating a port for the path", func() {
		// The scope key convention ties an allocation to its path; if it ever
		// changes, this assertion is what notices.
		allocation := model.ProxyPathPortAllocation{
			Kind: "path_internal_inbound", ScopeKey: itoa(path.ID) + ":0", ServerID: hop.ID,
			Pool: "public", ListenIP: "0.0.0.0", Network: "tcp_udp", Generation: 1, Ordinal: 0,
			Port: 20001, State: "active", PolicyRevision: 1,
		}
		if err := db.SaveProxyPathPortAllocations(ctx, []model.ProxyPathPortAllocation{allocation}, nil); err != nil {
			t.Fatal(err)
		}
	})

	// A node this user does not have must not disturb their key.
	stable := fingerprint()
	otherInbound.Name = "other-renamed"
	if err := db.UpdateInbound(ctx, otherInbound); err != nil {
		t.Fatal(err)
	}
	if fingerprint() != stable {
		t.Fatal("an unrelated node's edit republished this user's subscription")
	}
}
