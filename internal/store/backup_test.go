package store

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
)

func TestBackupCreatesReadableSnapshot(t *testing.T) {
	root := t.TempDir()
	db, err := Open(filepath.Join(root, "source.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.SetSetting(context.Background(), "backup-test", "present"); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(root, "backups", "snapshot.sqlite")
	if err := db.Backup(context.Background(), destination); err != nil {
		t.Fatal(err)
	}
	snapshot, err := Open(destination)
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	settings, err := snapshot.ListSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if settings["backup-test"] != "present" {
		t.Fatalf("snapshot setting = %q", settings["backup-test"])
	}
}

func TestBackupDuringConcurrentWrites(t *testing.T) {
	root := t.TempDir()
	sourcePath := filepath.Join(root, "source.sqlite")
	db, err := Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := db.SetSetting(ctx, "backup-base", "present"); err != nil {
		t.Fatal(err)
	}

	stop := make(chan struct{})
	writerErr := make(chan error, 1)
	var writer sync.WaitGroup
	writer.Add(1)
	go func() {
		defer writer.Done()
		for index := 0; ; index++ {
			select {
			case <-stop:
				return
			default:
			}
			if err := db.SetSetting(ctx, "backup-writer", fmt.Sprint(index)); err != nil {
				writerErr <- err
				return
			}
		}
	}()
	destination := filepath.Join(root, "backups", "snapshot.sqlite")
	backupErr := db.Backup(ctx, destination)
	close(stop)
	writer.Wait()
	close(writerErr)
	if backupErr != nil {
		t.Fatal(backupErr)
	}
	for err := range writerErr {
		t.Fatal(err)
	}

	snapshot, err := OpenForRestore(destination)
	if err != nil {
		t.Fatal(err)
	}
	if got := snapshot.DBStats().MaxOpenConnections; got != 1 {
		t.Fatalf("restore MaxOpenConnections = %d, want 1", got)
	}
	var mode string
	if err := snapshot.db.QueryRowContext(ctx, `pragma journal_mode`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "delete" {
		t.Fatalf("restore journal_mode = %q, want delete", mode)
	}
	if err := snapshot.CheckIntegrity(ctx); err != nil {
		t.Fatal(err)
	}
	settings, err := snapshot.ListSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if settings["backup-base"] != "present" {
		t.Fatalf("snapshot base setting = %q", settings["backup-base"])
	}
	if err := snapshot.Close(); err != nil {
		t.Fatal(err)
	}

	normal, err := Open(destination)
	if err != nil {
		t.Fatal(err)
	}
	defer normal.Close()
	if err := normal.db.QueryRowContext(ctx, `pragma journal_mode`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Fatalf("normal journal_mode after restore = %q, want wal", mode)
	}
}

func TestRewrapEncryptedSecretsIncludesGoogleEAB(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	const sourceSecret = "source-master-secret"
	const targetSecret = "target-master-secret"
	const hmacKey = "google-eab-hmac-key"
	encrypted, err := security.EncryptSecret(sourceSecret, "certificate-eab-hmac-key", hmacKey)
	if err != nil {
		t.Fatal(err)
	}
	certificate := &model.Certificate{
		Name: "google", PrimaryDomain: "example.com", Domains: []string{"example.com"}, ChallengeType: model.CertificateChallengeDNSManual,
		ACMECA: "google", EABKeyID: "google-key-id", EABHMACKeyEncrypted: encrypted, Status: model.CertificateStatusPending,
	}
	ctx := context.Background()
	if err := db.CreateCertificate(ctx, certificate); err != nil {
		t.Fatal(err)
	}
	savedEncrypted, err := security.EncryptSecret(sourceSecret, "google-eab-hmac-key", hmacKey)
	if err != nil {
		t.Fatal(err)
	}
	savedCredential := &model.GoogleEABCredential{KeyID: "saved-google-key-id", Remark: "backup", HMACKeyEncrypted: savedEncrypted}
	if err := db.CreateGoogleEABCredential(ctx, savedCredential); err != nil {
		t.Fatal(err)
	}
	if err := db.RewrapEncryptedSecrets(ctx, sourceSecret, targetSecret); err != nil {
		t.Fatal(err)
	}
	stored, err := db.GetCertificate(ctx, certificate.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !stored.EABConfigured || stored.EABHMACKeyEncrypted == encrypted {
		t.Fatalf("rewrapped certificate EAB = %#v", stored)
	}
	plain, err := security.DecryptSecret(targetSecret, "certificate-eab-hmac-key", stored.EABHMACKeyEncrypted)
	if err != nil || plain != hmacKey {
		t.Fatalf("decrypt rewrapped EAB HMAC: value=%q err=%v", plain, err)
	}
	savedStored, err := db.GetGoogleEABCredential(ctx, savedCredential.ID)
	if err != nil {
		t.Fatal(err)
	}
	savedPlain, err := security.DecryptSecret(targetSecret, "google-eab-hmac-key", savedStored.HMACKeyEncrypted)
	if err != nil || savedPlain != hmacKey || savedStored.HMACKeyEncrypted == savedEncrypted {
		t.Fatalf("decrypt rewrapped saved EAB HMAC: value=%q err=%v", savedPlain, err)
	}
}
