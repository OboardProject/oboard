package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func TestOpenRestrictsDatabaseFilePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oboard.sqlite")
	// Pre-create a world-readable placeholder so Open must tighten the mode.
	if err := os.WriteFile(path, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Fatalf("database permissions = %04o, want owner-only", perm)
	}
}

func TestServerTelemetryTimeColumnsMigrateFromPreviousSchema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "oboard.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	server := &model.Server{
		Name:           "migration-node",
		AgentID:        "migration-agent",
		AgentTokenHash: "migration-token-hash",
		Status:         model.ServerOnline,
	}
	if err := s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	timeColumns := []string{
		"time_correction_mode",
		"time_check_status",
		"time_offset_ms",
		"time_effective_offset_ms",
		"time_check_source",
		"time_check_error",
		"time_logical_active",
		"time_unsupported_paths_json",
		"time_checked_at",
	}
	for _, column := range timeColumns {
		if _, err := raw.Exec(`alter table server_telemetry drop column ` + column); err != nil {
			t.Fatalf("drop %s: %v", column, err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(path)
	if err != nil {
		t.Fatalf("open with previous telemetry schema: %v", err)
	}
	defer s.Close()
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("repeat migration: %v", err)
	}
	if err := s.CheckHealth(ctx); err != nil {
		t.Fatalf("health check after migration: %v", err)
	}

	columns := map[string]bool{}
	rows, err := s.db.QueryContext(ctx, `select name from pragma_table_info('server_telemetry')`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		columns[name] = true
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	for _, column := range timeColumns {
		if !columns[column] {
			t.Errorf("missing migrated column %q", column)
		}
	}

	var correctionMode, status, source, checkError, unsupportedPaths string
	var offset, effectiveOffset int64
	var logicalActive int
	var checkedAt sql.NullString
	if err := s.db.QueryRowContext(ctx, `select time_correction_mode,time_check_status,time_offset_ms,time_effective_offset_ms,time_check_source,time_check_error,time_logical_active,time_unsupported_paths_json,time_checked_at from server_telemetry where server_id=?`, server.ID).Scan(
		&correctionMode, &status, &offset, &effectiveOffset, &source, &checkError, &logicalActive, &unsupportedPaths, &checkedAt,
	); err != nil {
		t.Fatal(err)
	}
	if correctionMode != "off" || status != "unknown" || offset != 0 || effectiveOffset != 0 || source != "" || checkError != "" || logicalActive != 0 || unsupportedPaths != "[]" || checkedAt.Valid {
		t.Fatalf("migrated defaults = mode=%q status=%q offset=%d effective=%d source=%q error=%q logical=%d unsupported=%q checked=%v", correctionMode, status, offset, effectiveOffset, source, checkError, logicalActive, unsupportedPaths, checkedAt)
	}

	got, err := s.GetServerByAgent(ctx, server.AgentID)
	if err != nil {
		t.Fatalf("get server by agent after migration: %v", err)
	}
	if got.TimeCorrectionMode != model.TimeCorrectionOff || got.TimeCheckStatus != "unknown" {
		t.Fatalf("migrated server time state = mode=%q status=%q", got.TimeCorrectionMode, got.TimeCheckStatus)
	}
	checked := time.Date(2026, time.August, 1, 12, 45, 0, 0, time.UTC)
	result := model.TimeCheckResult{
		Status:               "corrected",
		RawOffsetMS:          45_000,
		EffectiveOffsetMS:    25,
		Source:               "ntp:test",
		CheckedAt:            checked,
		LogicalTimeActive:    true,
		UnsupportedTimePaths: []string{"mieru"},
	}
	if err := s.UpdateServerTimeCheck(ctx, server.ID, result); err != nil {
		t.Fatalf("update time check after migration: %v", err)
	}
	got, err = s.GetServerByAgent(ctx, server.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	if got.TimeCheckStatus != result.Status || got.TimeOffsetMS != result.RawOffsetMS || got.TimeEffectiveOffsetMS != result.EffectiveOffsetMS || got.TimeCheckSource != result.Source || !got.TimeLogicalActive || got.TimeCheckedAt == nil || !got.TimeCheckedAt.Equal(checked) || len(got.TimeUnsupportedPaths) != 1 || got.TimeUnsupportedPaths[0] != "mieru" {
		t.Fatalf("updated server time state = %#v", got)
	}
}

func TestServerListenModeAndInterfaceIPv6PersistAndMigrate(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "oboard.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	server := &model.Server{Name: "listen-mode", AgentID: "agent-listen", ListenMode: model.ListenModeDual, InterfaceIPv6: "2400:3200::1", Status: model.ServerOnline}
	if err := s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	stored, err := s.GetServer(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ListenMode != model.ListenModeDual || stored.InterfaceIPv6 != "2400:3200::1" {
		t.Fatalf("roundtrip = listen_mode=%q interface_ipv6=%q", stored.ListenMode, stored.InterfaceIPv6)
	}

	window := model.ServerTrafficWindow{Key: "2026-08-03", Start: time.Now().UTC(), End: time.Now().UTC().Add(time.Hour)}
	report := model.HealthReport{AgentID: server.AgentID, Status: model.ServerOnline, InterfaceIPv6: "2400:3200::2", Timestamp: time.Now().UTC()}
	if _, _, err := s.UpsertHealthTransition(ctx, report, window); err != nil {
		t.Fatal(err)
	}
	stored, err = s.GetServer(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.InterfaceIPv6 != "2400:3200::2" {
		t.Fatalf("health report did not overwrite interface_ipv6: %q", stored.InterfaceIPv6)
	}
	report.InterfaceIPv6 = ""
	if _, _, err := s.UpsertHealthTransition(ctx, report, window); err != nil {
		t.Fatal(err)
	}
	stored, err = s.GetServer(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.InterfaceIPv6 != "" {
		t.Fatalf("empty health report did not clear interface_ipv6: %q", stored.InterfaceIPv6)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{"listen_mode", "interface_ipv6"} {
		if _, err := raw.Exec(`alter table servers drop column ` + column); err != nil {
			t.Fatalf("drop %s: %v", column, err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("repeat migration: %v", err)
	}
	if err := s.CheckHealth(ctx); err != nil {
		t.Fatalf("health check after migration: %v", err)
	}
	columns := map[string]bool{}
	rows, err := s.db.QueryContext(ctx, `select name from pragma_table_info('servers')`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		columns[name] = true
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{"listen_mode", "interface_ipv6"} {
		if !columns[column] {
			t.Errorf("missing migrated column %q", column)
		}
	}
	stored, err = s.GetServer(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ListenMode != model.ListenModeAuto || stored.InterfaceIPv6 != "" {
		t.Fatalf("migrated defaults = listen_mode=%q interface_ipv6=%q", stored.ListenMode, stored.InterfaceIPv6)
	}
}

func TestProxyPathEgressAttemptsRetainOnlySameTopologySuccess(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	server := &model.Server{Name: "egress-owner"}
	if err := s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	inbound := &model.Inbound{ServerID: server.ID, Name: "entry", Protocol: model.ProtocolVLESS, Port: 443, ConfigJSON: "{}", Enabled: true}
	if err := s.CreateInbound(ctx, inbound); err != nil {
		t.Fatal(err)
	}
	external := &model.ExternalOutbound{Name: "imported", Protocol: model.ProtocolSocks, Scope: model.ExternalOutboundScopeGlobal, TargetAddress: "8.8.8.8", TargetPort: 1080, ConfigJSON: "{}", Enabled: true}
	if err := s.CreateExternalOutbound(ctx, external); err != nil {
		t.Fatal(err)
	}
	path := &model.ProxyPath{InboundID: inbound.ID, Enabled: true}
	if err := s.CreateProxyPath(ctx, path); err != nil {
		t.Fatal(err)
	}
	target := model.ExternalEgressProbeTarget{PathID: path.ID, ExternalOutboundID: external.ID, OwnerServerID: server.ID, TopologyFingerprint: "fingerprint-a"}
	succeededAt := time.Now().UTC().Add(-time.Minute)
	if err := s.SaveProxyPathEgressAttempt(ctx, target, 1, 0, "succeeded", "8.8.8.8", "US", "geo-v1", "", succeededAt); err != nil {
		t.Fatal(err)
	}
	failedAt := succeededAt.Add(time.Minute)
	if err := s.SaveProxyPathEgressAttempt(ctx, target, 1, 0, "failed", "", "", "", "timeout", failedAt); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetProxyPathEgressResult(ctx, target.PathID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "failed" || got.LastExitIP != "8.8.8.8" || got.LastRegionCode != "US" || got.GeoDatabaseRevision != "geo-v1" || got.LastSuccessAt == nil || !got.LastSuccessAt.Equal(succeededAt) {
		t.Fatalf("same-topology failure = %#v", got)
	}

	target.TopologyFingerprint = "fingerprint-b"
	if err := s.MarkProxyPathEgressPending(ctx, target, 2, 0); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetProxyPathEgressResult(ctx, target.PathID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "pending" || got.LastExitIP != "" || got.LastRegionCode != "" || got.GeoDatabaseRevision != "" || got.LastSuccessAt != nil {
		t.Fatalf("changed-topology pending result = %#v", got)
	}
}

func TestPasskeyOwnerLookupRequiresCredentialAndUserHandle(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	user := &model.User{Username: "passkey-owner", PasswordHash: "hash", Role: model.RoleAdmin, Status: "active", ProxyUUID: "uuid", ProxyPassword: "password"}
	if err := s.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnsureWebAuthnUserHandle(ctx, user.ID, "user-handle"); err != nil {
		t.Fatal(err)
	}
	credential := &model.PasskeyCredential{UserID: user.ID, Name: "test", CredentialID: "credential-id", CredentialJSON: `{}`}
	if err := s.CreatePasskeyCredential(ctx, credential); err != nil {
		t.Fatal(err)
	}
	owner, err := s.GetUserByPasskey(ctx, "credential-id", "user-handle")
	if err != nil || owner.ID != user.ID {
		t.Fatalf("passkey owner=%#v err=%v", owner, err)
	}
	if _, err := s.GetUserByPasskey(ctx, "credential-id", "other-handle"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("mismatched user handle lookup err=%v, want sql.ErrNoRows", err)
	}
}

func TestOpenPreservesAuthChallengesWhenUserBecomesOptional(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oboard.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	user := &model.User{Username: "challenge-owner", PasswordHash: "hash", Role: model.RoleAdmin, Status: "active", ProxyUUID: "uuid", ProxyPassword: "password"}
	if err := s.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	existing := model.AuthChallenge{TokenHash: "existing", Kind: "test", UserID: user.ID, DataEncrypted: "sealed", ExpiresAt: time.Now().UTC().Add(time.Hour)}
	if err := s.CreateAuthChallenge(ctx, existing); err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`drop index idx_auth_challenges_expiry`,
		`alter table auth_challenges rename to auth_challenges_optional_user`,
		`create table auth_challenges (token_hash text primary key, kind text not null, user_id integer not null references users(id) on delete cascade, data_encrypted text not null, expires_at text not null, created_at text not null)`,
		`insert into auth_challenges select * from auth_challenges_optional_user`,
		`drop table auth_challenges_optional_user`,
		`create index idx_auth_challenges_expiry on auth_challenges(expires_at)`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	stored, err := s.GetAuthChallenge(ctx, existing.TokenHash, existing.Kind)
	if err != nil || stored.UserID != user.ID || stored.DataEncrypted != existing.DataEncrypted {
		t.Fatalf("migrated challenge=%#v err=%v", stored, err)
	}
	anonymous := model.AuthChallenge{TokenHash: "anonymous", Kind: "test", DataEncrypted: "sealed-anonymous", ExpiresAt: time.Now().UTC().Add(time.Hour)}
	if err := s.CreateAuthChallenge(ctx, anonymous); err != nil {
		t.Fatal(err)
	}
	stored, err = s.GetAuthChallenge(ctx, anonymous.TokenHash, anonymous.Kind)
	if err != nil || stored.UserID != 0 {
		t.Fatalf("anonymous challenge=%#v err=%v", stored, err)
	}
}

func TestProxyPathLegacyNamesMigrateToAutomaticMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oboard.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`create table proxy_paths (id integer primary key autoincrement, name text not null, inbound_id integer not null, secret text not null default '', enabled integer not null default 1, created_at text not null, updated_at text not null)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`insert into proxy_paths(name,inbound_id,secret,enabled,created_at,updated_at) values('entry 分支 7',1,'secret',1,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	paths, err := s.ListProxyPaths(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || paths[0].NameMode != model.ProxyPathNameAuto || len(paths[0].NameTemplate) != 0 || paths[0].Name != "" {
		t.Fatalf("migrated path = %#v", paths)
	}
	created := model.ProxyPath{InboundID: 2, Enabled: true}
	if err := s.CreateProxyPath(context.Background(), &created); err != nil {
		t.Fatalf("create path after migration: %v", err)
	}
	if created.Kind != model.ProxyPathKindChain {
		t.Fatalf("default path kind = %q", created.Kind)
	}
	var legacyNameColumns int
	if err := s.db.QueryRow(`select count(*) from pragma_table_info('proxy_paths') where name='name'`).Scan(&legacyNameColumns); err != nil {
		t.Fatal(err)
	}
	if legacyNameColumns != 0 {
		t.Fatalf("legacy name column count = %d", legacyNameColumns)
	}
}

func TestDNSCredentialZonesPreserveRecordMetadataOnUpdate(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	credential := &model.DNSCredential{
		Name:            "shared-key",
		Provider:        model.DNSProviderCloudflare,
		ZoneName:        "example.com",
		ConfigEncrypted: "encrypted",
		Enabled:         true,
		Zones: []model.DNSCredentialZone{
			{ZoneName: "example.com"},
			{ZoneName: "oboard.proxy", ProviderZoneID: "zone-2"},
		},
	}
	if err := db.CreateDNSCredential(ctx, credential); err != nil {
		t.Fatal(err)
	}
	if len(credential.Zones) != 2 || credential.Zones[0].ID == 0 || credential.Zones[1].ID == 0 {
		t.Fatalf("zones were not persisted: %#v", credential.Zones)
	}
	record := model.DNSRecord{ID: "record-1", Comment: "OBoard edge"}
	if err := db.UpsertDNSRecordMetadata(ctx, credential.Zones[0].ID, record); err != nil {
		t.Fatal(err)
	}
	firstZoneID := credential.Zones[0].ID
	credential.Name = "shared-key-updated"
	if err := db.UpdateDNSCredential(ctx, credential); err != nil {
		t.Fatal(err)
	}
	stored, err := db.GetDNSCredential(ctx, credential.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Zones) != 2 || stored.Zones[0].ID != firstZoneID {
		t.Fatalf("unexpected stored zones: %#v", stored.Zones)
	}
	metadata, err := db.ListDNSRecordMetadata(ctx, firstZoneID)
	if err != nil {
		t.Fatal(err)
	}
	if metadata["record-1"].Comment != "OBoard edge" {
		t.Fatalf("record metadata was not preserved: %#v", metadata)
	}
}

func TestBootstrapAdminIsAtomicAcrossConnections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oboard.sqlite")
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()

	const attempts = 100
	results := make(chan error, attempts)
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			u := &model.User{
				Username:          fmt.Sprintf("admin-%d", i),
				PasswordHash:      "hash",
				Status:            "active",
				ProxyUUID:         fmt.Sprintf("uuid-%d", i),
				ProxyPassword:     fmt.Sprintf("password-%d", i),
				SubscriptionToken: fmt.Sprintf("subscription-%d", i),
			}
			db := first
			if i%2 == 1 {
				db = second
			}
			created, err := db.BootstrapAdmin(context.Background(), u)
			if err != nil {
				results <- err
				return
			}
			if created {
				results <- nil
				return
			}
			results <- sql.ErrNoRows
		}()
	}
	wg.Wait()
	close(results)

	created := 0
	for err := range results {
		if err == nil {
			created++
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("bootstrap failed: %v", err)
		}
	}
	if created != 1 {
		t.Fatalf("created admins = %d, want 1", created)
	}
	users, err := first.ListUsers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	admins := 0
	for _, user := range users {
		if user.Role == model.RoleAdmin {
			admins++
			isBootstrap, err := first.IsBootstrapAdmin(context.Background(), user.ID)
			if err != nil || !isBootstrap {
				t.Fatalf("created admin is not bootstrap identity: admin=%d bootstrap=%t err=%v", user.ID, isBootstrap, err)
			}
		}
	}
	if admins != 1 {
		t.Fatalf("stored admins = %d, want 1", admins)
	}
}

func TestForeignKeysEnabled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oboard.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	var enabled int
	if err := s.db.QueryRow(`pragma foreign_keys`).Scan(&enabled); err != nil || enabled != 1 {
		t.Fatalf("foreign_keys = %d, err=%v", enabled, err)
	}
	var fkCount int
	rows, err := s.db.Query(`pragma foreign_key_list(inbound_users)`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		fkCount++
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if fkCount != 2 {
		t.Fatalf("inbound_users foreign key count = %d, want 2", fkCount)
	}
	if _, err := s.db.Exec(`insert into inbound_users(inbound_id,user_id,enabled,created_at,updated_at) values(999,999,1,'x','x')`); err == nil {
		t.Fatal("fresh schema accepted orphan inbound user")
	}
	defer s.Close()
}

func TestSharedRateLimitIsAtomicAndBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oboard.sqlite")
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	ctx := context.Background()
	start := make(chan struct{})
	results := make(chan bool, 10)
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		db := first
		if i%2 == 1 {
			db = second
		}
		wg.Add(1)
		go func(db *Store) {
			defer wg.Done()
			<-start
			allowed, err := db.AllowRate(ctx, "shared-key", 3, time.Minute, 100)
			if err != nil {
				t.Errorf("AllowRate: %v", err)
				return
			}
			results <- allowed
		}(db)
	}
	close(start)
	wg.Wait()
	close(results)
	allowed := 0
	for result := range results {
		if result {
			allowed++
		}
	}
	if allowed != 3 {
		t.Fatalf("allowed requests = %d, want 3", allowed)
	}
	if allowed, err := first.AllowRate(ctx, "other-key", 1, time.Minute, 1); err != nil || allowed {
		t.Fatalf("new key at capacity = allowed:%t err:%v", allowed, err)
	}
}

func TestSubscriptionTokenBurnAfterReadIsAtomic(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	user := &model.User{
		Username:                  "one-time-user",
		PasswordHash:              "hash",
		Role:                      model.RoleViewer,
		Status:                    "active",
		ProxyUUID:                 "11111111-1111-4111-8111-111111111111",
		ProxyPassword:             "pass",
		SubscriptionToken:         "one-time-token",
		SubscriptionBurnAfterRead: true,
	}
	if err := s.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}

	stored, err := s.GetUserBySubscriptionToken(ctx, user.SubscriptionToken)
	if err != nil {
		t.Fatal(err)
	}
	if !stored.SubscriptionBurnAfterRead || stored.SubscriptionBurnedAt != nil {
		t.Fatalf("unexpected initial subscription policy: %#v", stored)
	}

	const attempts = 8
	results := make(chan error, attempts)
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			burned, err := s.ConsumeSubscriptionToken(ctx, user.ID, user.SubscriptionToken)
			if err == nil && !burned {
				err = errors.New("one-time token was not burned")
			}
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	succeeded := 0
	for err := range results {
		if err == nil {
			succeeded++
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("unexpected consume error: %v", err)
		}
	}
	if succeeded != 1 {
		t.Fatalf("successful consumes = %d, want 1", succeeded)
	}
	if _, err := s.GetUserBySubscriptionToken(ctx, user.SubscriptionToken); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("burned token still resolves: %v", err)
	}
	stored, err = s.GetUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.SubscriptionToken != "" || stored.SubscriptionBurnedAt == nil || !stored.SubscriptionBurnAfterRead {
		t.Fatalf("burned token state not persisted: %#v", stored)
	}

	if err := s.UpdateUserSubscriptionToken(ctx, user.ID, "replacement-token"); err != nil {
		t.Fatal(err)
	}
	stored, err = s.GetUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.SubscriptionToken != "replacement-token" || stored.SubscriptionBurnedAt != nil || !stored.SubscriptionBurnAfterRead {
		t.Fatalf("replacement did not preserve policy and reset burn state: %#v", stored)
	}
	if err := s.SetUserSubscriptionBurnAfterRead(ctx, user.ID, false); err != nil {
		t.Fatal(err)
	}
	burned, err := s.ConsumeSubscriptionToken(ctx, user.ID, "replacement-token")
	if err != nil || burned {
		t.Fatalf("persistent token consume = burned:%v err:%v", burned, err)
	}
	if _, err := s.GetUserBySubscriptionToken(ctx, "replacement-token"); err != nil {
		t.Fatalf("persistent token was invalidated: %v", err)
	}
}

func TestSubscriptionAgeKeyPersistsAcrossUserUpdates(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	user := &model.User{
		Username:                 "age-user",
		PasswordHash:             "hash",
		Role:                     model.RoleViewer,
		Status:                   "active",
		ProxyUUID:                "11111111-1111-4111-8111-111111111111",
		ProxyPassword:            "pass",
		SubscriptionToken:        "age-token",
		SubscriptionAgeEnabled:   true,
		SubscriptionAgePublicKey: "age1test-public-key",
	}
	if err := s.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	stored, err := s.GetUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !stored.SubscriptionAgeEnabled || stored.SubscriptionAgePublicKey != user.SubscriptionAgePublicKey {
		t.Fatalf("initial age settings not persisted: %#v", stored)
	}
	stored.Nickname = "updated"
	if err := s.UpdateUser(ctx, stored); err != nil {
		t.Fatal(err)
	}
	if err := s.SetUserSubscriptionAge(ctx, user.ID, false, user.SubscriptionAgePublicKey); err != nil {
		t.Fatal(err)
	}
	stored, err = s.GetUserByUsername(ctx, user.Username)
	if err != nil {
		t.Fatal(err)
	}
	if stored.SubscriptionAgeEnabled || stored.SubscriptionAgePublicKey != user.SubscriptionAgePublicKey || stored.Nickname != "updated" {
		t.Fatalf("updated age settings not persisted: %#v", stored)
	}
}

func TestStoreCRUDAndDashboard(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	user := &model.User{Username: "admin", PasswordHash: "hash", Role: model.RoleAdmin, Status: "active", ProxyUUID: "uuid", ProxyPassword: "pass", SubscriptionToken: "sub"}
	if err := s.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	server := &model.Server{Name: "s1", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10010, Status: model.ServerOnline}
	if err := s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	server2 := &model.Server{Name: "s2", PublicIPv4: "203.0.113.2", ListenIP: "0.0.0.0", PortRangeStart: 20000, PortRangeEnd: 20010, Status: model.ServerOnline}
	if err := s.CreateServer(ctx, server2); err != nil {
		t.Fatal(err)
	}
	if server.ChainSecret == "" || server2.ChainSecret == "" || server.ChainSecret == server2.ChainSecret {
		t.Fatalf("servers did not receive distinct persistent chain secrets: s1=%q s2=%q", server.ChainSecret, server2.ChainSecret)
	}
	storedServer, err := s.GetServer(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if storedServer.ChainSecret != server.ChainSecret {
		t.Fatalf("chain secret was not persisted: created=%q stored=%q", server.ChainSecret, storedServer.ChainSecret)
	}
	inbound := &model.Inbound{
		ServerID:        server.ID,
		Name:            "cf-entry",
		Protocol:        model.ProtocolSS,
		ListenIP:        "0.0.0.0",
		Port:            8388,
		EntryIPMode:     model.EntryIPModeCustom,
		ExternalIP:      "entry.example.com",
		DNSSyncEnabled:  true,
		DNSProxyEnabled: false,
		DNSRecordTypes:  "both",
		DNSSyncStatus:   "待同步",
		ConfigJSON:      `{"method":"2022-blake3-aes-128-gcm","password":"0123456789abcdef01234567"}`,
		Enabled:         true,
	}
	if err := s.CreateInbound(ctx, inbound); err != nil {
		t.Fatal(err)
	}
	group := &model.UserGroup{Name: "vip", Description: "fast users", Enabled: true, SpeedLimitMbps: 200, TrafficLimitBytes: 1 << 30}
	if err := s.CreateUserGroup(ctx, group); err != nil {
		t.Fatal(err)
	}
	group.SpeedLimitMbps = 100
	group.TrafficLimitBytes = 512 << 20
	if err := s.UpdateUserGroup(ctx, group); err != nil {
		t.Fatal(err)
	}
	storedGroup, err := s.GetUserGroup(ctx, group.ID)
	if err != nil {
		t.Fatal(err)
	}
	if storedGroup.SpeedLimitMbps != 100 || storedGroup.TrafficLimitBytes != 512<<20 {
		t.Fatalf("user group limits not persisted: %#v", storedGroup)
	}
	syncedAt := time.Now().UTC()
	if err := s.UpdateInboundDNSSyncResult(ctx, inbound.ID, "A 已新建", "", &syncedAt); err != nil {
		t.Fatal(err)
	}
	storedInbound, err := s.GetInbound(ctx, inbound.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !storedInbound.DNSSyncEnabled || storedInbound.DNSRecordTypes != "both" || storedInbound.DNSSyncStatus != "A 已新建" || storedInbound.DNSLastSyncedAt == nil {
		t.Fatalf("cloudflare fields not persisted: %#v", storedInbound)
	}
	forward := &model.PortForward{Name: "s1-to-s2", SourceServerID: server.ID, TargetServerID: server2.ID, ListenIP: "0.0.0.0", ListenPort: 443, TargetAddress: "203.0.113.2", TargetPort: 8443, Protocol: model.ForwardProtocolTCP, Backend: model.ForwardBackendAuto, Priority: 100, ConfigJSON: "{}", Enabled: true}
	if err := s.CreatePortForward(ctx, forward); err != nil {
		t.Fatal(err)
	}
	forward.TargetPort = 9443
	if err := s.UpdatePortForward(ctx, forward); err != nil {
		t.Fatal(err)
	}
	storedForward, err := s.GetPortForward(ctx, forward.ID)
	if err != nil {
		t.Fatal(err)
	}
	if storedForward.TargetPort != 9443 || storedForward.Backend != model.ForwardBackendAuto {
		t.Fatalf("bad port forward: %#v", storedForward)
	}
	now := time.Now().UTC()
	period := model.TrafficPeriod{UserID: user.ID, PeriodKey: now.Format("2006-01-02"), StartedAt: now.Add(-time.Hour), EndsAt: now.AddDate(0, 1, 0), Limit: 0}
	report := model.TrafficReport{ReportID: "dashboard-traffic", ServerID: server.ID, UserID: user.ID, PeriodKey: period.PeriodKey, Upload: 10, Download: 20, StartedAt: now, EndedAt: now}
	if _, err := s.AddTrafficReports(ctx, []model.TrafficReport{report}, period); err != nil {
		t.Fatal(err)
	}
	updated, err := s.GetUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.TrafficUsedBytes != 30 {
		t.Fatalf("traffic counter not updated: %d", updated.TrafficUsedBytes)
	}
	d, err := s.Dashboard(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if d.UsersTotal != 1 || d.ServersOnline != 2 || d.TrafficUpload != 10 || d.TrafficDownload != 20 {
		t.Fatalf("bad dashboard: %#v", d)
	}
}

func TestFailTimedOutTasks(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	server := &model.Server{Name: "offline", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10010, Status: model.ServerOffline}
	if err := s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	oldPending := &model.AgentTask{ServerID: server.ID, Type: "apply_core_config", PayloadJSON: "{}", Status: "pending", ResultJSON: "{}", Nonce: "pending-old"}
	oldRunning := &model.AgentTask{ServerID: server.ID, Type: "detect_mtu", PayloadJSON: "{}", Status: "pending", ResultJSON: "{}", Nonce: "running-old"}
	freshPending := &model.AgentTask{ServerID: server.ID, Type: "collect_logs", PayloadJSON: "{}", Status: "pending", ResultJSON: "{}", Nonce: "pending-fresh"}
	for _, task := range []*model.AgentTask{oldPending, oldRunning, freshPending} {
		if err := s.CreateTask(ctx, task); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-30 * time.Minute).UTC().Format(time.RFC3339Nano)
	if _, err := s.db.ExecContext(ctx, `update agent_tasks set updated_at=? where id in (?,?)`, old, oldPending.ID, oldRunning.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `update agent_tasks set status='running' where id=?`, oldRunning.ID); err != nil {
		t.Fatal(err)
	}
	failed, err := s.FailTimedOutTasks(ctx, time.Now().Add(-10*time.Minute), time.Now().Add(-20*time.Minute), `{"message":"pending timeout"}`, `{"message":"running timeout"}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(failed) != 2 {
		t.Fatalf("timed out rows = %d, want 2", len(failed))
	}
	tasks, err := s.ListTasksByServer(ctx, server.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[int64]model.AgentTask{}
	for _, task := range tasks {
		byID[task.ID] = task
	}
	if byID[oldPending.ID].Status != "failed" || byID[oldRunning.ID].Status != "failed" {
		t.Fatalf("old tasks not failed: %#v / %#v", byID[oldPending.ID], byID[oldRunning.ID])
	}
	if byID[freshPending.ID].Status != "pending" {
		t.Fatalf("fresh task should remain pending: %#v", byID[freshPending.ID])
	}
}

func TestAddTrafficReportsAcknowledgesDuplicateWithoutDoubleCounting(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	user := &model.User{Username: "traffic-user", PasswordHash: "hash", Role: model.RoleViewer, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "pass", SubscriptionToken: "sub"}
	if err := s.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	server := &model.Server{Name: "traffic-server", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10010, Status: model.ServerOnline}
	if err := s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	period := model.TrafficPeriod{UserID: user.ID, PeriodKey: "2026-07", StartedAt: now.Add(-time.Hour), EndsAt: now.Add(time.Hour), Limit: 1024}
	report := model.TrafficReport{ReportID: "report-once", ServerID: server.ID, UserID: user.ID, PeriodKey: period.PeriodKey, Upload: 10, Download: 20, StartedAt: now, EndedAt: now}

	for attempt := 0; attempt < 2; attempt++ {
		accepted, err := s.AddTrafficReports(ctx, []model.TrafficReport{report}, period)
		if err != nil {
			t.Fatal(err)
		}
		if len(accepted) != 1 || accepted[0] != report.ReportID {
			t.Fatalf("attempt %d accepted = %#v", attempt, accepted)
		}
	}

	stored, err := s.GetUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.TrafficUsedBytes != 30 {
		t.Fatalf("traffic used = %d, want 30", stored.TrafficUsedBytes)
	}
}

func TestTrafficLeaseAllocationPreservesOfflineResetBudget(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	user := &model.User{Username: "lease-user", PasswordHash: "hash", Role: model.RoleViewer, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "pass", SubscriptionToken: "lease-sub"}
	if err := s.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	servers := []model.Server{
		{Name: "lease-a", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10010, Status: model.ServerOnline},
		{Name: "lease-b", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10010, Status: model.ServerOnline},
	}
	for i := range servers {
		if err := s.CreateServer(ctx, &servers[i]); err != nil {
			t.Fatal(err)
		}
	}
	first, err := s.EnsureTrafficLeaseAllocation(ctx, servers[0].ID, user.ID, "2026-07", 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.EnsureTrafficLeaseAllocation(ctx, servers[1].ID, user.ID, "2026-07", 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if first.RemainingBytes != 100 || first.ResetBytes != 100 {
		t.Fatalf("first allocation = %#v, want full 100 byte budget", first)
	}
	if second.RemainingBytes != 0 || second.ResetBytes != 0 {
		t.Fatalf("second allocation = %#v, want an enforced zero budget", second)
	}
}

func TestTrafficLeaseAllocationIsAtomicAcrossStoreConnections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oboard.sqlite")
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	ctx := context.Background()
	user := &model.User{Username: "concurrent-lease-user", PasswordHash: "hash", Role: model.RoleViewer, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111112", ProxyPassword: "pass", SubscriptionToken: "concurrent-lease-sub"}
	if err := first.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	servers := []model.Server{
		{Name: "concurrent-lease-a", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10010, Status: model.ServerOnline},
		{Name: "concurrent-lease-b", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10010, Status: model.ServerOnline},
	}
	for index := range servers {
		if err := first.CreateServer(ctx, &servers[index]); err != nil {
			t.Fatal(err)
		}
	}

	start := make(chan struct{})
	allocations := make(chan TrafficLeaseAllocation, 2)
	errorsCh := make(chan error, 2)
	var wg sync.WaitGroup
	for index, database := range []*Store{first, second} {
		index, database := index, database
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			allocation, err := database.EnsureTrafficLeaseAllocation(ctx, servers[index].ID, user.ID, "2026-07", 100, 0)
			if err != nil {
				errorsCh <- err
				return
			}
			allocations <- allocation
		}()
	}
	close(start)
	wg.Wait()
	close(allocations)
	close(errorsCh)
	for err := range errorsCh {
		t.Fatal(err)
	}
	var total int64
	for allocation := range allocations {
		total += allocation.RemainingBytes
	}
	if total != 100 {
		t.Fatalf("concurrent lease total = %d, want 100", total)
	}
}

func TestTrafficLeaseAllocationUsesDurableAcceptedUsage(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	user := &model.User{Username: "durable-lease-user", PasswordHash: "hash", Role: model.RoleViewer, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111113", ProxyPassword: "pass", SubscriptionToken: "durable-lease-sub"}
	if err := s.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	server := &model.Server{Name: "durable-lease-server", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10010, Status: model.ServerOnline}
	if err := s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	period := model.TrafficPeriod{UserID: user.ID, PeriodKey: "2026-07", StartedAt: time.Now().Add(-time.Hour), EndsAt: time.Now().Add(time.Hour), Limit: 100}
	report := model.TrafficReport{ReportID: "durable-before-lease", ServerID: server.ID, UserID: user.ID, PeriodKey: period.PeriodKey, Upload: 80, StartedAt: time.Now().Add(-time.Minute), EndedAt: time.Now()}
	if accepted, err := s.AddTrafficReports(ctx, []model.TrafficReport{report}, period); err != nil || len(accepted) != 1 {
		t.Fatalf("add traffic report accepted=%v err=%v", accepted, err)
	}
	allocation, err := s.EnsureTrafficLeaseAllocation(ctx, server.ID, user.ID, period.PeriodKey, period.Limit, 0)
	if err != nil {
		t.Fatal(err)
	}
	if allocation.RemainingBytes != 20 || allocation.ResetBytes != 100 {
		t.Fatalf("allocation = %#v, want remaining=20 reset=100", allocation)
	}
}

func TestDashboardReturnsDatabaseErrors(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Dashboard(context.Background()); err == nil {
		t.Fatal("expected dashboard query to fail after database close")
	}
}

func TestRecordDNSBenchmarkResultRollsBackWhenPolicyUpdateFails(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	server := &model.Server{Name: "dns-server", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10010, Status: model.ServerOnline}
	if err := s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	policy, _ := s.GetServerDNSPolicy(ctx, server.ID)
	encrypted, _ := s.GetDNSList(ctx, policy.EncryptedListID)
	bootstrap, _ := s.GetDNSList(ctx, policy.BootstrapListID)
	if _, err := s.db.ExecContext(ctx, `create trigger fail_dns_policy_update before update on server_dns_policies begin select raise(abort, 'policy update failed'); end`); err != nil {
		t.Fatal(err)
	}
	result := successfulDNSBenchmarkResult(*policy, *encrypted, *bootstrap, "rollback-report")
	if _, err := s.RecordDNSBenchmarkResult(ctx, &result); err == nil {
		t.Fatal("expected policy update error")
	}
	results, err := s.ListDNSBenchmarkResults(ctx, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Fatalf("benchmark result should be rolled back: %#v", results)
	}
}

func TestDNSListRevisionInvalidatesOnlyReferencedSelection(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	server := &model.Server{Name: "dns-server", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10010, Status: model.ServerOnline}
	if err := s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	policy, _ := s.GetServerDNSPolicy(ctx, server.ID)
	encrypted, _ := s.GetDNSList(ctx, policy.EncryptedListID)
	bootstrap, _ := s.GetDNSList(ctx, policy.BootstrapListID)
	result := successfulDNSBenchmarkResult(*policy, *encrypted, *bootstrap, "initial-report")
	if _, err := s.RecordDNSBenchmarkResult(ctx, &result); err != nil {
		t.Fatal(err)
	}
	custom := &model.DNSList{Name: "custom encrypted", Kind: model.DNSListEncrypted, Revision: 1, Enabled: true, Candidates: append([]model.DNSCandidate(nil), encrypted.Candidates...)}
	if err := s.CreateDNSList(ctx, custom); err != nil {
		t.Fatal(err)
	}
	policy.EncryptedListID = custom.ID
	if err := s.UpdateServerDNSPolicy(ctx, policy); err != nil {
		t.Fatal(err)
	}
	result = successfulDNSBenchmarkResult(*policy, *custom, *bootstrap, "custom-report")
	if _, err := s.RecordDNSBenchmarkResult(ctx, &result); err != nil {
		t.Fatal(err)
	}
	custom.Candidates[0].Server = "dns.cloudflare.com"
	changed, err := s.UpdateDNSList(ctx, custom)
	if err != nil {
		t.Fatal(err)
	}
	if !changed || custom.Revision != 2 {
		t.Fatalf("updated list = %#v, changed=%v", custom, changed)
	}
	got, err := s.GetServerDNSPolicy(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.EncryptedSelected) != 0 || got.EncryptedSelectionRevision != 0 || len(got.BootstrapSelected) != 2 || !got.NeedsBenchmark {
		t.Fatalf("invalidated policy = %#v", got)
	}
}

func TestDNSPolicyInvalidationKeepsSelectionArraysNonNull(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	server := &model.Server{Name: "dns-server", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10010, Status: model.ServerOnline}
	if err := s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	policy, err := s.GetServerDNSPolicy(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := s.GetDNSList(ctx, policy.EncryptedListID)
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, err := s.GetDNSList(ctx, policy.BootstrapListID)
	if err != nil {
		t.Fatal(err)
	}
	customEncrypted := &model.DNSList{Name: "custom encrypted", Kind: model.DNSListEncrypted, Revision: 1, Enabled: true, Candidates: append([]model.DNSCandidate(nil), encrypted.Candidates...)}
	customBootstrap := &model.DNSList{Name: "custom bootstrap", Kind: model.DNSListBootstrap, Revision: 1, Enabled: true, Candidates: append([]model.DNSCandidate(nil), bootstrap.Candidates...)}
	if err := s.CreateDNSList(ctx, customEncrypted); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateDNSList(ctx, customBootstrap); err != nil {
		t.Fatal(err)
	}
	policy.EncryptedListID = customEncrypted.ID
	policy.BootstrapListID = customBootstrap.ID
	if err := s.UpdateServerDNSPolicy(ctx, policy); err != nil {
		t.Fatal(err)
	}
	if policy.EncryptedSelected == nil || policy.BootstrapSelected == nil {
		t.Fatalf("invalidated selections must be empty arrays: %#v", policy)
	}
	encoded, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatal(err)
	}
	if string(payload["encrypted_selected"]) != "[]" || string(payload["bootstrap_selected"]) != "[]" {
		t.Fatalf("invalidated selections encoded as %s and %s", payload["encrypted_selected"], payload["bootstrap_selected"])
	}

	if _, err := s.db.ExecContext(ctx, `update server_dns_policies set encrypted_selected_json='null',bootstrap_selected_json='null' where server_id=?`, server.ID); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.GetServerDNSPolicy(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.EncryptedSelected == nil || loaded.BootstrapSelected == nil {
		t.Fatalf("stored null selections were not normalized: %#v", loaded)
	}
}

func TestDefaultDNSListsAreEditableAndReferencedDeletionConflicts(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	lists, err := s.ListDNSLists(ctx, false)
	if err != nil || len(lists) != 2 {
		t.Fatalf("default lists = %#v, err=%v", lists, err)
	}
	for _, list := range lists {
		if !list.Protected || !list.Enabled {
			t.Fatalf("default list is not protected: %#v", list)
		}
		oldRevision := list.Revision
		list.Name += " changed"
		list.Candidates[0].Tag += "-changed"
		list.Enabled = false
		changed, err := s.UpdateDNSList(ctx, &list)
		if err != nil || !changed || !list.Protected || !list.Enabled || list.Revision != oldRevision+1 {
			t.Fatalf("updated default list = %#v, changed=%v, err=%v", list, changed, err)
		}
		if err := s.DeleteDNSList(ctx, list.ID); err == nil {
			t.Fatalf("protected list %d was deleted", list.ID)
		}
	}
	custom := &model.DNSList{Name: "custom encrypted", Kind: model.DNSListEncrypted, Revision: 1, Enabled: true, Candidates: append([]model.DNSCandidate(nil), lists[0].Candidates...)}
	if custom.Candidates[0].Transport == model.DNSTransportUDP {
		custom.Candidates = append([]model.DNSCandidate(nil), lists[1].Candidates...)
	}
	if err := s.CreateDNSList(ctx, custom); err != nil {
		t.Fatal(err)
	}
	server := &model.Server{Name: "dns-ref", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10010, Status: model.ServerOnline}
	if err := s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	policy, _ := s.GetServerDNSPolicy(ctx, server.ID)
	policy.EncryptedListID = custom.ID
	if err := s.UpdateServerDNSPolicy(ctx, policy); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteDNSList(ctx, custom.ID); err == nil {
		t.Fatal("referenced dns list was deleted")
	}
}

func TestCurrentDNSSchemaSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oboard.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	lists, _ := s.ListDNSLists(context.Background(), false)
	var encrypted model.DNSList
	for _, list := range lists {
		if list.Kind == model.DNSListEncrypted {
			encrypted = list
		}
	}
	custom := &model.DNSList{Name: "persisted dns", Kind: model.DNSListEncrypted, Revision: 1, Enabled: true, Candidates: append([]model.DNSCandidate(nil), encrypted.Candidates...)}
	if err := s.CreateDNSList(context.Background(), custom); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.GetDNSList(context.Background(), custom.ID); err != nil {
		t.Fatalf("current dns schema was reset on reopen: %v", err)
	}
}

func TestLegacyDNSSchemaIsDestructivelyReset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oboard.sqlite")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`create table dns_profiles(id integer primary key, name text); create table dns_benchmark_results(id integer primary key, profile_id integer)`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var legacyTables int
	if err := s.db.QueryRow(`select count(*) from sqlite_master where type='table' and name='dns_profiles'`).Scan(&legacyTables); err != nil {
		t.Fatal(err)
	}
	lists, err := s.ListDNSLists(context.Background(), false)
	if err != nil || legacyTables != 0 || len(lists) != 2 {
		t.Fatalf("legacy reset tables=%d lists=%#v err=%v", legacyTables, lists, err)
	}
}

func TestStaleDNSBenchmarkIsStoredWithoutUpdatingPolicy(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	server := &model.Server{Name: "dns-stale", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10010, Status: model.ServerOnline}
	if err := s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	policy, _ := s.GetServerDNSPolicy(ctx, server.ID)
	encrypted, _ := s.GetDNSList(ctx, policy.EncryptedListID)
	bootstrap, _ := s.GetDNSList(ctx, policy.BootstrapListID)
	result := successfulDNSBenchmarkResult(*policy, *encrypted, *bootstrap, "stale-report")
	policy.Strategy = "prefer_ipv6"
	if err := s.UpdateServerDNSPolicy(ctx, policy); err != nil {
		t.Fatal(err)
	}
	outcome, err := s.RecordDNSBenchmarkResult(ctx, &result)
	if err != nil || !outcome.Stale || outcome.Success {
		t.Fatalf("stale outcome = %#v, err=%v", outcome, err)
	}
	stored, _ := s.GetServerDNSPolicy(ctx, server.ID)
	if len(stored.EncryptedSelected) != 0 || len(stored.BootstrapSelected) != 0 || stored.LastAttemptAt != nil {
		t.Fatalf("stale result updated policy: %#v", stored)
	}
	history, _ := s.ListDNSBenchmarkResults(ctx, server.ID, 10)
	if len(history) != 1 || history[0].Status != "stale" {
		t.Fatalf("stale history = %#v", history)
	}
}

func TestFailedDNSGroupPreservesLastSuccessfulSelections(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	server := &model.Server{Name: "dns-failure", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10010, Status: model.ServerOnline}
	if err := s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	policy, _ := s.GetServerDNSPolicy(ctx, server.ID)
	encrypted, _ := s.GetDNSList(ctx, policy.EncryptedListID)
	bootstrap, _ := s.GetDNSList(ctx, policy.BootstrapListID)
	success := successfulDNSBenchmarkResult(*policy, *encrypted, *bootstrap, "success-report")
	if _, err := s.RecordDNSBenchmarkResult(ctx, &success); err != nil {
		t.Fatal(err)
	}
	failure := successfulDNSBenchmarkResult(*policy, *encrypted, *bootstrap, "failure-report")
	failure.Bootstrap = model.DNSBenchmarkGroup{Items: []model.DNSBenchmarkItem{{Tag: bootstrap.Candidates[0].Tag, LatencyMS: 2000, Error: "timeout"}}}
	outcome, err := s.RecordDNSBenchmarkResult(ctx, &failure)
	if err != nil || outcome.Success || outcome.Stale {
		t.Fatalf("failure outcome = %#v, err=%v", outcome, err)
	}
	stored, _ := s.GetServerDNSPolicy(ctx, server.ID)
	if len(stored.EncryptedSelected) != 2 || len(stored.BootstrapSelected) != 2 || !stored.NeedsBenchmark || stored.LastError == "" {
		t.Fatalf("failed result replaced selections: %#v", stored)
	}
}

func successfulDNSBenchmarkResult(policy model.ServerDNSPolicy, encrypted, bootstrap model.DNSList, reportID string) model.DNSBenchmarkResult {
	return model.DNSBenchmarkResult{
		ReportID: reportID, ServerID: policy.ServerID, PolicyRevision: policy.Revision,
		EncryptedListID: encrypted.ID, EncryptedListRevision: encrypted.Revision,
		BootstrapListID: bootstrap.ID, BootstrapListRevision: bootstrap.Revision,
		Encrypted: model.DNSBenchmarkGroup{Items: []model.DNSBenchmarkItem{{Tag: encrypted.Candidates[0].Tag, LatencyMS: 10}, {Tag: encrypted.Candidates[1].Tag, LatencyMS: 20}}, BestTags: []string{encrypted.Candidates[0].Tag, encrypted.Candidates[1].Tag}},
		Bootstrap: model.DNSBenchmarkGroup{Items: []model.DNSBenchmarkItem{{Tag: bootstrap.Candidates[0].Tag, LatencyMS: 5}, {Tag: bootstrap.Candidates[1].Tag, LatencyMS: 8}}, BestTags: []string{bootstrap.Candidates[0].Tag, bootstrap.Candidates[1].Tag}},
	}
}

func TestAddMTUDetectionResultRollsBackWhenServerUpdateFails(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	server := &model.Server{Name: "mtu-server", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10010, Status: model.ServerOnline}
	if err := s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `create trigger fail_mtu_server_update before update on servers begin select raise(abort, 'server update failed'); end`); err != nil {
		t.Fatal(err)
	}
	if err := s.AddMTUDetectionResult(ctx, model.MTUDetectionResult{ServerID: server.ID, RecommendedMTU: 1400, ResultJSON: "{}"}); err == nil {
		t.Fatal("expected server update error")
	}
	results, err := s.ListMTUDetectionResults(ctx, server.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Fatalf("MTU result should be rolled back: %#v", results)
	}
}

func TestNextTaskReturnsErrorWhenClaimFails(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	server := &model.Server{Name: "task-server", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10010, Status: model.ServerOnline}
	if err := s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	task := &model.AgentTask{ServerID: server.ID, Type: "apply_core_config", PayloadJSON: "{}", Status: "pending", ResultJSON: "{}", Nonce: "claim-failure"}
	if err := s.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `create trigger fail_task_claim before update on agent_tasks begin select raise(abort, 'claim failed'); end`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.NextTask(ctx, server.ID); err == nil {
		t.Fatal("expected task claim error")
	}
	stored, err := s.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != "pending" {
		t.Fatalf("task status = %q, want pending", stored.Status)
	}
}

func TestNextTaskDoubleConsumerClaimsOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oboard.sqlite")
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	ctx := context.Background()
	server := &model.Server{Name: "task-server", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10010, Status: model.ServerOnline}
	if err := first.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	task := &model.AgentTask{ServerID: server.ID, Type: "apply_core_config", PayloadJSON: "{}", Status: "pending", ResultJSON: "{}", Nonce: "double-consumer"}
	if err := first.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, db := range []*Store{first, second} {
		db := db
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			claimed, err := db.NextTask(ctx, server.ID)
			if err == nil && claimed.ID != task.ID {
				err = fmt.Errorf("claimed task %d, want %d", claimed.ID, task.ID)
			}
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	claimed := 0
	for err := range results {
		if err == nil {
			claimed++
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("unexpected claim error: %v", err)
		}
	}
	if claimed != 1 {
		t.Fatalf("successful claims = %d, want 1", claimed)
	}
}

func TestLatestDeploymentTasksUsesLatestDeploymentVersion(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	server := &model.Server{Name: "deployment-server", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10010, Status: model.ServerOnline}
	if err := s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	for _, task := range []*model.AgentTask{
		{ServerID: server.ID, Type: model.AgentTaskTypeApplyDeployment, PayloadJSON: "{}", Status: "succeeded", ResultJSON: "{}", ConfigVersion: 100, Nonce: "deploy-old"},
		{ServerID: server.ID, Type: model.AgentTaskTypeApplyDeployment, PayloadJSON: "{}", Status: "pending", ResultJSON: "{}", ConfigVersion: 150, Nonce: "deploy-new"},
		{ServerID: server.ID, Type: model.AgentTaskTypeApplyDeployment, PayloadJSON: "{}", Status: "succeeded", ResultJSON: "{}", ConfigVersion: 150, Nonce: "deploy-sibling"},
		{ServerID: server.ID, Type: "collect_logs", PayloadJSON: "{}", Status: "succeeded", ResultJSON: "{}", ConfigVersion: 200, Nonce: "non-deployment"},
	} {
		if err := s.CreateTask(ctx, task); err != nil {
			t.Fatal(err)
		}
	}
	tasks, err := s.LatestDeploymentTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 2 {
		t.Fatalf("latest deployment task count = %d, want 2", len(tasks))
	}
	for _, task := range tasks {
		if task.ConfigVersion != 150 {
			t.Fatalf("config version = %d, want 150", task.ConfigVersion)
		}
	}
}

func TestNextConfigVersionIsMonotonicAndUnique(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oboard.sqlite")
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	ctx := context.Background()
	const count = 32
	versions := make(chan int64, count)
	errorsCh := make(chan error, count)
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		db := first
		if i%2 == 1 {
			db = second
		}
		wg.Add(1)
		go func(store *Store) {
			defer wg.Done()
			version, err := store.NextConfigVersion(ctx)
			if err != nil {
				errorsCh <- err
				return
			}
			versions <- version
		}(db)
	}
	wg.Wait()
	close(versions)
	close(errorsCh)
	for err := range errorsCh {
		t.Fatal(err)
	}
	seen := map[int64]bool{}
	var maximum int64
	for version := range versions {
		if seen[version] {
			t.Fatalf("duplicate config version %d", version)
		}
		seen[version] = true
		if version > maximum {
			maximum = version
		}
	}
	if len(seen) != count {
		t.Fatalf("allocated %d unique versions, want %d", len(seen), count)
	}
	next, err := first.NextConfigVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if next <= maximum {
		t.Fatalf("next config version %d is not greater than prior maximum %d", next, maximum)
	}
}

func TestLastSuccessfulConfigTaskUsesCoreRefreshBaseline(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	server := &model.Server{Name: "config-baseline", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10010, Status: model.ServerOnline}
	if err := s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	for _, task := range []*model.AgentTask{
		{ServerID: server.ID, Type: model.AgentTaskTypeApplyDeployment, PayloadJSON: `{"version":100}`, Status: "succeeded", ResultJSON: "{}", ConfigVersion: 100, Nonce: "deployment"},
		{ServerID: server.ID, Type: model.AgentTaskTypeApplyCoreConfig, PayloadJSON: `{"config":"new"}`, Status: "succeeded", ResultJSON: "{}", ConfigVersion: 101, Nonce: "core-refresh"},
		{ServerID: server.ID, Type: model.AgentTaskTypeApplyDeployment, PayloadJSON: `{"version":102}`, Status: "failed", ResultJSON: "{}", ConfigVersion: 102, Nonce: "failed-deployment"},
	} {
		if err := s.CreateTask(ctx, task); err != nil {
			t.Fatal(err)
		}
	}
	last, err := s.LastSuccessfulConfigTaskByServer(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if last.Type != model.AgentTaskTypeApplyCoreConfig || last.ConfigVersion != 101 {
		t.Fatalf("last successful config task = %#v", last)
	}
}

func TestDeploymentFailureDismissalIsScopedToConfigVersion(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()

	if err := s.DismissDeploymentFailure(ctx, 100, 7); err != nil {
		t.Fatal(err)
	}
	item, err := s.GetDeploymentFailureDismissal(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if item.ConfigVersion != 100 || item.ActorID != 7 || item.DismissedAt.IsZero() {
		t.Fatalf("unexpected dismissal: %#v", item)
	}
	if err := s.DismissDeploymentFailure(ctx, 100, 9); err != nil {
		t.Fatal(err)
	}
	item, err = s.GetDeploymentFailureDismissal(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if item.ActorID != 9 {
		t.Fatalf("dismissal actor = %d, want 9", item.ActorID)
	}
	if _, err := s.GetDeploymentFailureDismissal(ctx, 101); err == nil {
		t.Fatal("next deployment version must not inherit the dismissal")
	}
}

func TestClaimServerEnrollmentConsumesTokenOnce(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	server := &model.Server{Name: "node-1", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10010, Status: model.ServerUnknown}
	if err := s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	hash := "enrollment-hash-value"
	if err := s.SetServerEnrollmentHash(ctx, server.ID, hash, time.Now().UTC().Add(30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimServerEnrollment(ctx, hash, "agent-1", "token-hash-1")
	if err != nil {
		t.Fatal(err)
	}
	if claimed.AgentID != "agent-1" || claimed.AgentTokenHash != "token-hash-1" || claimed.EnrollmentHash != "" {
		t.Fatalf("unexpected claim result: %#v", claimed)
	}
	if _, err := s.ClaimServerEnrollment(ctx, hash, "agent-2", "token-hash-2"); err == nil {
		t.Fatal("expected second claim to fail after enrollment hash is consumed")
	}
	stored, err := s.GetServer(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.AgentID != "agent-1" || stored.EnrollmentHash != "" {
		t.Fatalf("stored server after claim = %#v", stored)
	}
}

func TestCompleteTaskRequiresPendingOrRunning(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	server := &model.Server{Name: "node-1", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10010, Status: model.ServerOnline}
	if err := s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	task := &model.AgentTask{ServerID: server.ID, Type: "apply_core_config", PayloadJSON: "{}", Status: "pending", ResultJSON: "{}", Nonce: "n1"}
	if err := s.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteTask(ctx, task.ID, "succeeded", `{"ok":true}`); err != nil {
		t.Fatal(err)
	}
	// Idempotent when already in the requested terminal status.
	if err := s.CompleteTask(ctx, task.ID, "succeeded", `{"ok":true}`); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteTask(ctx, task.ID, "failed", `{"ok":false}`); err == nil {
		t.Fatal("expected complete with different terminal status to fail")
	}
	terminal := &model.AgentTask{ServerID: server.ID, Type: "apply_core_config", PayloadJSON: "{}", Status: "succeeded", ResultJSON: `{"skipped":true}`, Nonce: "n2"}
	if err := s.CreateTask(ctx, terminal); err != nil {
		t.Fatal(err)
	}
	if terminal.CompletedAt == nil {
		t.Fatal("expected CreateTask to set completed_at for terminal status")
	}
}

func TestClaimServerEnrollmentRejectsExpiredToken(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	server := &model.Server{Name: "node-exp", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10010, Status: model.ServerUnknown}
	if err := s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	hash := "expired-enrollment-hash"
	if err := s.SetServerEnrollmentHash(ctx, server.ID, hash, time.Now().UTC().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimServerEnrollment(ctx, hash, "agent-x", "token-hash-x"); err == nil {
		t.Fatal("expected expired enrollment claim to fail")
	}
}

func TestBumpSessionVersionIncrements(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	user := &model.User{Username: "u1", PasswordHash: "hash", Role: model.RoleViewer, Status: "active", ProxyUUID: "uuid", ProxyPassword: "pass", SubscriptionToken: "sub"}
	if err := s.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	v1, err := s.BumpSessionVersion(ctx, user.ID)
	if err != nil || v1 != 1 {
		t.Fatalf("v1=%d err=%v", v1, err)
	}
	v2, err := s.BumpSessionVersion(ctx, user.ID)
	if err != nil || v2 != 2 {
		t.Fatalf("v2=%d err=%v", v2, err)
	}
	got, err := s.GetUser(ctx, user.ID)
	if err != nil || got.SessionVersion != 2 {
		t.Fatalf("stored version=%d err=%v", got.SessionVersion, err)
	}
}

func TestServerTelemetryRatesPeriodsAndHistory(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	server := &model.Server{Name: "metrics", AgentID: "agent-metrics", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10010, Status: model.ServerOnline, MonitoringMode: "standard", TrafficResetMode: "month_day", TrafficResetDay: 15, ConnectivityProbeEnabled: true}
	if err := s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)
	window := model.ServerTrafficWindow{Key: "2026-07-15", Start: start, End: start.AddDate(0, 1, 0)}
	first := model.HealthReport{AgentID: server.AgentID, Status: model.ServerOnline, CPUUsagePercent: 10, MemoryUsedBytes: 100, MemoryTotalBytes: 1000, NetworkTotalUploadBytes: 1000, NetworkTotalDownloadBytes: 2000, NetworkUploadBPS: 60, NetworkDownloadBPS: 120, ConnectivityProbeEnabled: true, ConnectivityAvailable: true, ConnectivityLatencyMS: 35, ConnectivityCheckedAt: start, Timestamp: start}
	if _, _, err := s.UpsertHealthTransition(ctx, first, window); err != nil {
		t.Fatal(err)
	}
	second := first
	second.Timestamp = start.Add(10 * time.Second)
	second.NetworkTotalUploadBytes = 1600
	second.NetworkTotalDownloadBytes = 3200
	second.ConnectivityCheckedAt = second.Timestamp
	if _, _, err := s.UpsertHealthTransition(ctx, second, window); err != nil {
		t.Fatal(err)
	}
	stored, err := s.GetServer(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.MonitoringMode != "standard" || stored.TrafficResetMode != "month_day" || stored.TrafficResetDay != 15 || !stored.ConnectivityProbeEnabled {
		t.Fatalf("telemetry settings = %#v", stored)
	}
	if stored.TrafficUploadBytes != 600 || stored.TrafficDownloadBytes != 1200 {
		t.Fatalf("period traffic upload=%d download=%d", stored.TrafficUploadBytes, stored.TrafficDownloadBytes)
	}
	if stored.NetworkUploadBPS != 60 || stored.NetworkDownloadBPS != 120 || stored.ConnectivityLatencyMS != 35 {
		t.Fatalf("live telemetry = %#v", stored)
	}
	samples, err := s.ListServerMetricSamples(ctx, server.ID, 10)
	if err != nil || len(samples) != 2 {
		t.Fatalf("samples=%d err=%v", len(samples), err)
	}
	nextStart := start.AddDate(0, 1, 0)
	nextWindow := model.ServerTrafficWindow{Key: "2026-08-15", Start: nextStart, End: nextStart.AddDate(0, 1, 0)}
	second.Timestamp = nextStart
	second.NetworkTotalUploadBytes = 5000
	second.NetworkTotalDownloadBytes = 9000
	if _, _, err := s.UpsertHealthTransition(ctx, second, nextWindow); err != nil {
		t.Fatal(err)
	}
	stored, err = s.GetServer(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.TrafficUploadBytes != 0 || stored.TrafficDownloadBytes != 0 {
		t.Fatalf("new period should start at zero: upload=%d download=%d", stored.TrafficUploadBytes, stored.TrafficDownloadBytes)
	}
}

func proxyPathTruncationFixture(t *testing.T, s *Store) (int64, int64, int64, int64) {
	t.Helper()
	ctx := context.Background()
	root := &model.Server{Name: "root", ListenIP: "0.0.0.0", PortRangeStart: 30000, PortRangeEnd: 30100, Status: model.ServerOnline}
	if err := s.CreateServer(ctx, root); err != nil {
		t.Fatal(err)
	}
	mid := &model.Server{Name: "mid", PublicIPv4: "203.0.113.5", ListenIP: "0.0.0.0", PortRangeStart: 31000, PortRangeEnd: 31100, Status: model.ServerOnline}
	if err := s.CreateServer(ctx, mid); err != nil {
		t.Fatal(err)
	}
	exit := &model.Server{Name: "exit", PublicIPv4: "203.0.113.9", ListenIP: "0.0.0.0", PortRangeStart: 32000, PortRangeEnd: 32100, Status: model.ServerOnline}
	if err := s.CreateServer(ctx, exit); err != nil {
		t.Fatal(err)
	}
	rootInbound := &model.Inbound{ServerID: root.ID, Name: "root-entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: "{}", Enabled: true}
	if err := s.CreateInbound(ctx, rootInbound); err != nil {
		t.Fatal(err)
	}
	hopInbound := &model.Inbound{ServerID: mid.ID, Name: "mid-entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 8443, ConfigJSON: "{}", Enabled: true}
	if err := s.CreateInbound(ctx, hopInbound); err != nil {
		t.Fatal(err)
	}
	path := &model.ProxyPath{InboundID: rootInbound.ID, Secret: "seed", Enabled: true}
	if err := s.CreateProxyPath(ctx, path); err != nil {
		t.Fatal(err)
	}
	first := &model.ProxyPathStep{PathID: path.ID, Position: 1, NodeType: model.ProxyPathStepServerInbound, TransportMode: model.ProxyPathTransportSingBox, InboundID: &hopInbound.ID, ServerID: &mid.ID, ConfigJSON: "{}"}
	if err := s.CreateProxyPathStep(ctx, first); err != nil {
		t.Fatal(err)
	}
	second := &model.ProxyPathStep{PathID: path.ID, Position: 2, NodeType: model.ProxyPathStepServerInbound, TransportMode: model.ProxyPathTransportSingBox, ServerID: &exit.ID, ConfigJSON: "{}"}
	if err := s.CreateProxyPathStep(ctx, second); err != nil {
		t.Fatal(err)
	}
	return path.ID, hopInbound.ID, first.ID, second.ID
}

func TestDeleteProxyPathsForInboundTruncatesLaterSteps(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	pathID, hopInboundID, _, _ := proxyPathTruncationFixture(t, s)

	if err := s.DeleteProxyPathsForInbound(ctx, hopInboundID); err != nil {
		t.Fatal(err)
	}
	steps, err := s.ListProxyPathStepsForPath(ctx, pathID)
	if err != nil {
		t.Fatal(err)
	}
	// Removing the hop must cut the chain there. Keeping the later step would
	// silently rewire the path to a topology the operator never selected.
	if len(steps) != 0 {
		t.Fatalf("steps after cutting the middle hop = %d, want 0: %#v", len(steps), steps)
	}
	paths, err := s.ListProxyPaths(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		if path.ID == pathID {
			t.Fatalf("path %d survived without any step", pathID)
		}
	}
}

func TestProxyPathBranchSourceClearsWhenSourceStepIsDeleted(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	pathID, _, sourceStepID, _ := proxyPathTruncationFixture(t, s)
	paths, err := s.ListProxyPaths(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var inboundID int64
	for _, path := range paths {
		if path.ID == pathID {
			inboundID = path.InboundID
			break
		}
	}
	direct := &model.ProxyPath{Kind: model.ProxyPathKindDirect, BranchSourceStepID: &sourceStepID, InboundID: inboundID, Secret: "direct", Enabled: true}
	if err := s.CreateProxyPath(ctx, direct); err != nil {
		t.Fatal(err)
	}
	stored, err := s.GetProxyPath(ctx, direct.ID)
	if err != nil || stored.BranchSourceStepID == nil || *stored.BranchSourceStepID != sourceStepID {
		t.Fatalf("stored direct branch=%#v err=%v", stored, err)
	}
	if err := s.DeleteProxyPathStepsFromPosition(ctx, pathID, 1); err != nil {
		t.Fatal(err)
	}
	stored, err = s.GetProxyPath(ctx, direct.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.BranchSourceStepID != nil {
		t.Fatalf("branch source survived deleted step: %#v", stored)
	}
}

func TestDeleteProxyPathStepsForExternalTruncatesLaterSteps(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	server := &model.Server{Name: "root", ListenIP: "0.0.0.0", PortRangeStart: 30000, PortRangeEnd: 30100, Status: model.ServerOnline}
	if err := s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	exit := &model.Server{Name: "exit", PublicIPv4: "203.0.113.9", ListenIP: "0.0.0.0", PortRangeStart: 32000, PortRangeEnd: 32100, Status: model.ServerOnline}
	if err := s.CreateServer(ctx, exit); err != nil {
		t.Fatal(err)
	}
	inbound := &model.Inbound{ServerID: server.ID, Name: "entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: "{}", Enabled: true}
	if err := s.CreateInbound(ctx, inbound); err != nil {
		t.Fatal(err)
	}
	external := &model.ExternalOutbound{Name: "socks-a", Protocol: model.ProtocolSocks, TargetAddress: "198.51.100.7", TargetPort: 1080, Scope: model.ExternalOutboundScopeGlobal, ConfigJSON: "{}", Enabled: true}
	if err := s.CreateExternalOutbound(ctx, external); err != nil {
		t.Fatal(err)
	}
	path := &model.ProxyPath{InboundID: inbound.ID, Secret: "seed", Enabled: true}
	if err := s.CreateProxyPath(ctx, path); err != nil {
		t.Fatal(err)
	}
	imported := &model.ProxyPathStep{PathID: path.ID, Position: 1, NodeType: model.ProxyPathStepImported, TransportMode: model.ProxyPathTransportSingBox, ExternalOutboundID: &external.ID, ConfigJSON: "{}"}
	if err := s.CreateProxyPathStep(ctx, imported); err != nil {
		t.Fatal(err)
	}
	later := &model.ProxyPathStep{PathID: path.ID, Position: 2, NodeType: model.ProxyPathStepServerInbound, TransportMode: model.ProxyPathTransportSingBox, ServerID: &exit.ID, ConfigJSON: "{}"}
	if err := s.CreateProxyPathStep(ctx, later); err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteProxyPathStepsForExternal(ctx, external.ID); err != nil {
		t.Fatal(err)
	}
	steps, err := s.ListProxyPathStepsForPath(ctx, path.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 0 {
		t.Fatalf("steps after removing the imported hop = %d, want 0: %#v", len(steps), steps)
	}
}

func TestProxyPathStepPositionsAreCompactedAndUnique(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oboard.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`create table proxy_path_steps (id integer primary key autoincrement, path_id integer not null, position integer not null, node_type text not null, transport_mode text not null default 'singbox', processing_role integer not null default 0, server_id integer, inbound_id integer, external_outbound_id integer, config_json text not null default '{}', created_at text not null, updated_at text not null)`); err != nil {
		t.Fatal(err)
	}
	// Two rows share one position, as a concurrent create could produce before
	// the unique index existed.
	for _, position := range []int{1, 2, 2, 5} {
		if _, err := db.Exec(`insert into proxy_path_steps(path_id,position,node_type,created_at,updated_at) values(1,?,'server_inbound','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`, position); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatalf("migration must repair duplicate positions: %v", err)
	}
	defer s.Close()
	steps, err := s.ListProxyPathStepsForPath(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 4 {
		t.Fatalf("step count = %d, want 4", len(steps))
	}
	for index, step := range steps {
		if step.Position != index+1 {
			t.Fatalf("positions were not compacted: %#v", steps)
		}
	}
	if _, err := s.db.Exec(`insert into proxy_path_steps(path_id,position,node_type,created_at,updated_at) values(1,1,'server_inbound','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err == nil {
		t.Fatal("duplicate (path_id, position) must be rejected by the unique index")
	}
}

func TestSetDefaultDNSListPerKind(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	lists, err := s.ListDNSLists(ctx, false)
	if err != nil || len(lists) != 2 {
		t.Fatalf("default lists = %#v, err=%v", lists, err)
	}
	var encrypted, bootstrap model.DNSList
	for _, list := range lists {
		switch list.Kind {
		case model.DNSListEncrypted:
			encrypted = list
		case model.DNSListBootstrap:
			bootstrap = list
		}
	}
	custom := &model.DNSList{Name: "custom encrypted", Kind: model.DNSListEncrypted, Revision: 1, Enabled: true, Candidates: append([]model.DNSCandidate(nil), encrypted.Candidates...)}
	if err := s.CreateDNSList(ctx, custom); err != nil {
		t.Fatal(err)
	}
	updated, err := s.SetDefaultDNSList(ctx, custom.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !updated.Protected || !updated.Enabled {
		t.Fatalf("promoted default list = %#v", updated)
	}
	after, _ := s.ListDNSLists(ctx, false)
	var encryptedDefaults, bootstrapDefaults int
	for _, list := range after {
		if !list.Protected {
			continue
		}
		if list.Kind == model.DNSListEncrypted {
			encryptedDefaults++
			if list.ID != custom.ID {
				t.Fatalf("old default still protected: %#v", list)
			}
		} else {
			bootstrapDefaults++
		}
	}
	if encryptedDefaults != 1 || bootstrapDefaults != 1 {
		t.Fatalf("defaults per kind = encrypted %d, bootstrap %d", encryptedDefaults, bootstrapDefaults)
	}
	server := &model.Server{Name: "dns-default-ref", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10010, Status: model.ServerOnline}
	if err := s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	policy, err := s.GetServerDNSPolicy(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if policy.EncryptedListID != custom.ID || policy.BootstrapListID != bootstrap.ID {
		t.Fatalf("new server policy = %#v", policy)
	}
	disabled := &model.DNSList{Name: "disabled encrypted", Kind: model.DNSListEncrypted, Revision: 1, Enabled: true, Candidates: append([]model.DNSCandidate(nil), custom.Candidates...)}
	if err := s.CreateDNSList(ctx, disabled); err != nil {
		t.Fatal(err)
	}
	disabled.Enabled = false
	if _, err := s.UpdateDNSList(ctx, disabled); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetDefaultDNSList(ctx, disabled.ID); err == nil {
		t.Fatal("disabled dns list was set as default")
	}
	if _, err := s.SetDefaultDNSList(ctx, 999999); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing dns list error = %v", err)
	}
}
