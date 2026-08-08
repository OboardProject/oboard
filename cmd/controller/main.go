package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/OboardProject/oboard/internal/backup"
	"github.com/OboardProject/oboard/internal/controller"
	oboardlog "github.com/OboardProject/oboard/internal/logging"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/store"
	"github.com/OboardProject/oboard/internal/version"
)

const defaultListenAddress = ":2787"

func main() {
	showVersion := flag.Bool("version", false, "print version and exit")
	showVersionJSON := flag.Bool("version-json", false, "print machine-readable version and exit")
	addr := flag.String("addr", env("OBOARD_ADDR", defaultListenAddress), "HTTP listen address")
	dbPath := flag.String("db", env("OBOARD_DB", "./data/oboard.sqlite"), "SQLite database path")
	staticDir := flag.String("static", env("OBOARD_STATIC", "./web/dist"), "web static directory")
	basePath := flag.String("base-path", env("OBOARD_BASE_PATH", ""), "URL path prefix for every Controller endpoint, for example /abc")
	secret := flag.String("session-secret", env("OBOARD_SESSION_SECRET", ""), "session signing secret")
	autoAdmin := flag.Bool("auto-admin", envBool("OBOARD_AUTO_ADMIN", true), "create the first admin automatically when no admin exists")
	adminUsername := flag.String("admin-username", env("OBOARD_ADMIN_USERNAME", "admin"), "first admin username when auto-admin is enabled")
	adminPassword := flag.String("admin-password", env("OBOARD_ADMIN_PASSWORD", ""), "first admin password; empty generates a random one-time password printed once")
	flag.Parse()
	if *showVersionJSON {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"version": version.Version, "build": version.Build, "commit": version.Commit, "date": version.Date})
		return
	}
	if *showVersion {
		log.Println("OBoard Controller", version.String())
		return
	}
	logPath := env("OBOARD_LOG_FILE", filepath.Join(filepath.Dir(*dbPath), "logs", "controller.log"))
	logManager, err := oboardlog.New(logPath, oboardlog.Config{MaxBytes: int64(envInt("OBOARD_LOG_MAX_MB", 32)) << 20, Backups: envInt("OBOARD_LOG_BACKUPS", 5)})
	if err != nil {
		log.Fatal(err)
	}
	defer logManager.Close()
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds | log.LUTC)
	log.SetOutput(io.MultiWriter(os.Stdout, logManager))
	if err := validateSessionSecret(*secret); err != nil {
		log.Fatal(err)
	}
	normalizedBasePath, err := controller.NormalizeBasePath(*basePath)
	if err != nil {
		log.Fatal(err)
	}
	if dbDir := filepath.Dir(*dbPath); dbDir != "." && dbDir != "" {
		if err := os.MkdirAll(dbDir, 0o750); err != nil {
			log.Fatal(err)
		}
	}
	backupDir := env("OBOARD_BACKUP_DIR", filepath.Join(filepath.Dir(*dbPath), "backups"))
	acmeHome := env("OBOARD_ACME_HOME", filepath.Join(filepath.Dir(*dbPath), "acme"))
	if err := backup.ApplyPendingRestore(backup.Config{Root: backupDir, DatabasePath: *dbPath, ACMEHome: acmeHome, MasterSecret: *secret}); err != nil {
		log.Fatal(err)
	}
	sqliteOptions := store.DefaultSQLiteOptions()
	sqliteOptions.MaxOpenConns = envIntRange("OBOARD_SQLITE_MAX_OPEN_CONNS", sqliteOptions.MaxOpenConns, 1, 16)
	sqliteOptions.MaxIdleConns = sqliteOptions.MaxOpenConns
	sqliteOptions.BusyTimeout = time.Duration(envIntRange("OBOARD_SQLITE_BUSY_TIMEOUT_MS", int(sqliteOptions.BusyTimeout/time.Millisecond), 1000, 30000)) * time.Millisecond
	db, err := store.OpenWithOptions(*dbPath, sqliteOptions)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	if *autoAdmin {
		if err := ensureFirstAdmin(context.Background(), db, *adminUsername, *adminPassword); err != nil {
			log.Fatal(err)
		}
	}
	app := controller.New(db, *secret, *staticDir, normalizedBasePath, logManager)
	defer app.Close()
	geoIPDir := env("OBOARD_GEOIP_DIR", filepath.Join(filepath.Dir(filepath.Dir(*dbPath)), "downloads", "geoip"))
	if err := app.ConfigureGeoIP(geoIPDir); err != nil {
		log.Printf("configure IP geolocation: %v", err)
	}
	app.ConfigureControllerUpdates(*dbPath, *addr)
	app.ConfigureControllerBackups(*dbPath)
	if err := app.ApplyRuntimeSettings(context.Background()); err != nil {
		log.Printf("apply runtime settings: %v", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	aiWorkerSocket := env("OBOARD_AI_WORKER_SOCKET", "/run/oboard/ai-worker/rpc.sock")
	if err := app.StartAIWorkerRPC(ctx, aiWorkerSocket); err != nil {
		log.Printf("configure AI Worker RPC: %v", err)
	}
	app.SetControllerBackupRestart(stop)
	go app.StartMonitor(ctx)
	go app.StartDNSDDNS(ctx)
	go app.StartCertificateRenewal(ctx)
	go app.StartControllerUpdates(ctx)
	go app.StartControllerBackups(ctx)
	go app.StartAccessChangeWorker(ctx)
	go app.StartAccessLifecycleWorker(ctx)
	srv := &http.Server{
		Addr:              *addr,
		Handler:           app.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("controller shutdown: %v", err)
		}
	}()
	log.Printf("OBoard controller listening on %s%s", *addr, app.BasePath())
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	switch os.Getenv(key) {
	case "1", "true", "TRUE", "yes", "YES", "on", "ON":
		return true
	case "0", "false", "FALSE", "no", "NO", "off", "OFF":
		return false
	default:
		return fallback
	}
}

func envInt(key string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(key)))
	if err != nil || value < 0 {
		return fallback
	}
	return value
}

func envIntRange(key string, fallback, minimum, maximum int) int {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(key)))
	if err != nil || value < minimum || value > maximum {
		return fallback
	}
	return value
}

const minSessionSecretLen = 32

func validateSessionSecret(value string) error {
	secret := strings.TrimSpace(value)
	if secret == "" {
		return errors.New("OBOARD_SESSION_SECRET is required")
	}
	// Session secret also keys AES-GCM for DNS credentials and certificate
	// private keys, so reject short values that are trivial to brute-force.
	if len(secret) < minSessionSecretLen {
		return fmt.Errorf("OBOARD_SESSION_SECRET must be at least %d characters", minSessionSecretLen)
	}
	return nil
}

func ensureFirstAdmin(ctx context.Context, db *store.Store, username, password string) error {
	if username == "" {
		username = "admin"
	}
	passwordFromEnv := strings.TrimSpace(password) != ""
	if !passwordFromEnv {
		// Never default to a well-known password. Generate a one-time random
		// password and print it once so first-boot installs remain secure.
		generated, err := security.RandomToken(18)
		if err != nil {
			return err
		}
		password = generated
	}
	hash, err := security.HashPassword(password)
	if err != nil {
		return err
	}
	uid, err := security.RandomUUID()
	if err != nil {
		return err
	}
	upass, err := security.RandomToken(18)
	if err != nil {
		return err
	}
	sub, err := security.RandomToken(24)
	if err != nil {
		return err
	}
	u := &model.User{Username: username, Nickname: username, PasswordHash: hash, Role: model.RoleAdmin, Status: "active", ProxyUUID: uid, ProxyPassword: upass, SubscriptionToken: sub}
	created, err := db.BootstrapAdmin(ctx, u)
	if err != nil {
		return err
	}
	if !created {
		return nil
	}
	_ = db.AddAudit(ctx, model.AuditLog{ActorID: &u.ID, Action: "auto_admin", Target: "user", Detail: username})
	log.Println("============================================================")
	log.Println("OBoard first administrator has been created")
	log.Printf("Username: %s", username)
	if passwordFromEnv {
		log.Println("Password: using OBOARD_ADMIN_PASSWORD from environment; not printed.")
	} else {
		log.Printf("One-time password: %s", password)
		log.Println("This password is shown only once. Log in and change it immediately.")
	}
	log.Println("============================================================")
	return nil
}
