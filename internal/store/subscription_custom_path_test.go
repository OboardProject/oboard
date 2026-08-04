package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

func TestSubscriptionCustomPathStorageAndCredentialRevocation(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	first := &model.User{Username: "custom-one", PasswordHash: "hash", Role: model.RoleViewer, Status: "active", ProxyUUID: "custom-one-uuid", ProxyPassword: "password", SubscriptionToken: "persistent-one"}
	second := &model.User{Username: "custom-two", PasswordHash: "hash", Role: model.RoleViewer, Status: "active", ProxyUUID: "custom-two-uuid", ProxyPassword: "password", SubscriptionToken: "persistent-two"}
	if err := s.CreateUser(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateUser(ctx, second); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetSubscriptionCustomPath(ctx, first.ID, "friendly-one"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetSubscriptionCustomPath(ctx, second.ID, "friendly-one"); !IsSubscriptionCustomPathConflict(err) {
		t.Fatalf("duplicate alias error = %v", err)
	}
	if err := s.CreateOneTimeSubscriptionToken(ctx, first.ID, "quick-one"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetUserSubscriptionBurnAfterRead(ctx, first.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeSubscriptionCredentials(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	stored, err := s.GetUser(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.SubscriptionToken != "" || !stored.SubscriptionBurnAfterRead {
		t.Fatalf("revocation changed unexpected state: %#v", stored)
	}
	if _, err := s.GetSubscriptionCustomPathForUser(ctx, first.ID); err == nil {
		t.Fatal("custom path survived revocation")
	}
	if _, err := s.GetUserBySubscriptionToken(ctx, "quick-one"); err == nil {
		t.Fatal("one-time token survived revocation")
	}
}
