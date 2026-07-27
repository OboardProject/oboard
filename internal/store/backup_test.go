package store

import (
	"context"
	"path/filepath"
	"testing"

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
