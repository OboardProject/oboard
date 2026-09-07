package store

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

func TestBootstrapAdminConcurrentRequestsCreateSingleAdmin(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "bootstrap.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	var wg sync.WaitGroup
	created := make([]bool, 20)
	errs := make([]error, 20)
	for i := range created {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			user := &model.User{
				Username:          fmt.Sprintf("admin-%d", index),
				Nickname:          "admin",
				PasswordHash:      "hash",
				Role:              model.RoleAdmin,
				Status:            "active",
				ProxyUUID:         fmt.Sprintf("11111111-1111-4111-8111-%012d", index),
				ProxyPassword:     "password",
				SubscriptionToken: fmt.Sprintf("token-%d", index),
			}
			created[index], errs[index] = db.BootstrapAdmin(ctx, user)
		}(i)
	}
	wg.Wait()
	ok := 0
	for i, item := range created {
		if errs[i] != nil {
			t.Fatalf("bootstrap %d: %v", i, errs[i])
		}
		if item {
			ok++
		}
	}
	if ok != 1 {
		t.Fatalf("created %d admins, want 1", ok)
	}
	users, err := db.ListUsers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	admins := 0
	for _, user := range users {
		if user.Role == model.RoleAdmin {
			admins++
		}
	}
	if admins != 1 {
		t.Fatalf("stored admin count=%d", admins)
	}
}

