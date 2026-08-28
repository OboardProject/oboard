package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	if err := db.Backup(context.Background(), destination, BackupOptions{}); err != nil {
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
	backupErr := db.Backup(ctx, destination, BackupOptions{})
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

func TestBackupReportsProgressAndHonorsCancel(t *testing.T) {
	root := t.TempDir()
	db, err := Open(filepath.Join(root, "source.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if _, err := db.db.ExecContext(ctx, `create table if not exists backup_fill (id integer primary key, blob blob not null)`); err != nil {
		t.Fatal(err)
	}
	chunk := make([]byte, 256<<10)
	for i := 0; i < 40; i++ {
		if _, err := db.db.ExecContext(ctx, `insert into backup_fill(blob) values(?)`, chunk); err != nil {
			t.Fatal(err)
		}
	}
	var reports []BackupProgress
	destination := filepath.Join(root, "progress.sqlite")
	if err := db.Backup(ctx, destination, BackupOptions{PagesPerStep: 16, Progress: func(progress BackupProgress) {
		reports = append(reports, progress)
	}}); err != nil {
		t.Fatal(err)
	}
	if len(reports) == 0 || reports[len(reports)-1].Percent < 99 {
		t.Fatalf("backup progress = %#v", reports)
	}
	if _, err := os.Stat(destination); err != nil {
		t.Fatal(err)
	}

	cancelDest := filepath.Join(root, "cancelled.sqlite")
	cancelCtx, cancel := context.WithCancel(context.Background())
	first := true
	err = db.Backup(cancelCtx, cancelDest, BackupOptions{PagesPerStep: 8, Progress: func(BackupProgress) {
		if first {
			first = false
			cancel()
		}
	}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled backup err = %v", err)
	}
	if _, statErr := os.Stat(cancelDest); !os.IsNotExist(statErr) {
		t.Fatalf("partial backup was left behind: %v", statErr)
	}
}

func TestBackupDoesNotUseVacuumInto(t *testing.T) {
	source, err := os.ReadFile("backup.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(source)), "vacuum into") {
		t.Fatal("online backup still contains VACUUM INTO")
	}
}

func TestClampBackupPercentNeverGoesBackwards(t *testing.T) {
	if got := clampBackupPercent(40, 100, 80, false); got != 40 {
		t.Fatalf("clamped decreasing remaining = %v, want 40", got)
	}
	if got := clampBackupPercent(10, 100, 50, false); got != 50 {
		t.Fatalf("forward percent = %v, want 50", got)
	}
	if got := clampBackupPercent(99, 100, 0, false); got != 99 {
		t.Fatalf("almost done stays below 100 = %v", got)
	}
	if got := clampBackupPercent(40, 100, 0, true); got != 100 {
		t.Fatalf("done percent = %v, want 100", got)
	}
}

func TestBackupAllowsConcurrentWrites(t *testing.T) {
	root := t.TempDir()
	db, err := Open(filepath.Join(root, "source.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := db.SetSetting(ctx, "backup-concurrent", "start"); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		var writeErr error
		for i := 0; i < 32; i++ {
			writeErr = db.SetSetting(ctx, fmt.Sprintf("k-%d", i), "v")
			if writeErr != nil {
				break
			}
		}
		done <- writeErr
	}()
	destination := filepath.Join(root, "snapshot.sqlite")
	if err := db.Backup(ctx, destination, BackupOptions{PagesPerStep: 8}); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("writes during backup: %v", err)
	}
	snapshot, err := Open(destination)
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	if _, err := snapshot.ListSettings(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestBackupCopiesHundredMegabytes(t *testing.T) {
	if testing.Short() {
		t.Skip("100MB backup is skipped in short mode")
	}
	root := t.TempDir()
	db, err := Open(filepath.Join(root, "source.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if _, err := db.db.ExecContext(ctx, `create table if not exists backup_blob (id integer primary key, data blob)`); err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, 100<<20)
	if _, err := db.db.ExecContext(ctx, `insert into backup_blob(data) values(?)`, payload); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(root, "large.sqlite")
	if err := db.Backup(ctx, destination, BackupOptions{}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(destination)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() < 90<<20 {
		t.Fatalf("backup size = %d, want at least 90MiB", info.Size())
	}
}

func TestBackupCopiesOneGigabyte(t *testing.T) {
	if os.Getenv("OBOARD_BACKUP_LARGE_TEST") != "1" {
		t.Skip("set OBOARD_BACKUP_LARGE_TEST=1 to run the 1GB backup test")
	}
	root := t.TempDir()
	db, err := Open(filepath.Join(root, "source.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if _, err := db.db.ExecContext(ctx, `create table if not exists backup_blob (id integer primary key, data blob)`); err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, 1<<30)
	if _, err := db.db.ExecContext(ctx, `insert into backup_blob(data) values(?)`, payload); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(root, "huge.sqlite")
	if err := db.Backup(ctx, destination, BackupOptions{}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(destination)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() < 900<<20 {
		t.Fatalf("backup size = %d, want at least 900MiB", info.Size())
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
