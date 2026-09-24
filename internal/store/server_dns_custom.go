package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
)

// ErrServerOwnedDNSList rejects shared-list management of a server's custom
// resolver list; that list changes only through the server's DNS policy.
var ErrServerOwnedDNSList = errors.New("dns list holds a server's custom resolvers and is managed through that server's dns policy")

// ensureServerDNSListOwner adds dns_lists.owner_server_id, which marks the
// custom resolver list of one server. Existing lists stay shared (NULL).
func (s *Store) ensureServerDNSListOwner(ctx context.Context) error {
	if err := s.ensureColumn(ctx, "dns_lists", "owner_server_id", `alter table dns_lists add column owner_server_id integer`); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `create unique index if not exists idx_dns_lists_owner_kind on dns_lists(owner_server_id, kind) where owner_server_id is not null`)
	return err
}

func (s *Store) UpdateServerDNSPolicy(ctx context.Context, v *model.ServerDNSPolicy) error {
	if _, err := s.EnsureServerDNSPolicy(ctx, v.ServerID); err != nil {
		return err
	}
	if strings.TrimSpace(v.Strategy) == "" {
		v.Strategy = "auto"
	}
	if v.AutoTest == "" {
		v.AutoTest = model.DNSAutoTestFirstApply
	}
	if v.TestIntervalSeconds == 0 {
		v.TestIntervalSeconds = 3600
	}
	if v.AutoTest == model.DNSAutoTestPeriodic && v.TestIntervalSeconds < 300 {
		return errors.New("periodic dns test interval must be at least 300 seconds")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	current, err := scanDNSPolicy(tx.QueryRowContext(ctx, dnsPolicySelectSQL+` where server_id=?`, v.ServerID))
	if err != nil {
		return err
	}
	ts := now()
	encryptedID, encryptedContentChanged, err := bindServerDNSGroupTx(ctx, tx, v.ServerID, model.DNSListEncrypted, v.EncryptedListID, v.EncryptedCandidates, ts)
	if err != nil {
		return fmt.Errorf("encrypted resolvers: %w", err)
	}
	bootstrapID, bootstrapContentChanged, err := bindServerDNSGroupTx(ctx, tx, v.ServerID, model.DNSListBootstrap, v.BootstrapListID, v.BootstrapCandidates, ts)
	if err != nil {
		return fmt.Errorf("bootstrap resolvers: %w", err)
	}
	encryptedChanged := encryptedContentChanged || current.EncryptedListID != encryptedID
	bootstrapChanged := bootstrapContentChanged || current.BootstrapListID != bootstrapID
	listChanged := encryptedChanged || bootstrapChanged
	changed := listChanged || current.Strategy != v.Strategy || current.AutoTest != v.AutoTest || current.TestIntervalSeconds != v.TestIntervalSeconds
	if changed {
		next := *current
		next.EncryptedListID, next.BootstrapListID = encryptedID, bootstrapID
		next.Strategy, next.AutoTest, next.TestIntervalSeconds = v.Strategy, v.AutoTest, v.TestIntervalSeconds
		next.Revision = current.Revision + 1
		if encryptedChanged {
			next.EncryptedSelected = []model.DNSCandidate{}
			next.EncryptedSelectionRevision = 0
		}
		if bootstrapChanged {
			next.BootstrapSelected = []model.DNSCandidate{}
			next.BootstrapSelectionRevision = 0
		}
		if listChanged {
			next.LastError = ""
			next.NeedsBenchmark = true
		}
		encJSON, _ := json.Marshal(next.EncryptedSelected)
		bootstrapJSON, _ := json.Marshal(next.BootstrapSelected)
		if _, err := tx.ExecContext(ctx, `update server_dns_policies set encrypted_list_id=?,bootstrap_list_id=?,revision=?,strategy=?,auto_test=?,test_interval_seconds=?,encrypted_selected_json=?,bootstrap_selected_json=?,encrypted_selection_revision=?,bootstrap_selection_revision=?,last_error=?,needs_benchmark=?,updated_at=? where server_id=?`, zeroToNull(next.EncryptedListID), next.BootstrapListID, next.Revision, next.Strategy, next.AutoTest, next.TestIntervalSeconds, string(encJSON), string(bootstrapJSON), next.EncryptedSelectionRevision, next.BootstrapSelectionRevision, next.LastError, boolInt(next.NeedsBenchmark), ts, v.ServerID); err != nil {
			return err
		}
	}
	// A custom list the policy no longer binds belongs to nobody; drop it after
	// the policy row stops referencing it.
	if _, err := tx.ExecContext(ctx, `delete from dns_lists where owner_server_id=? and id not in (?,?)`, v.ServerID, encryptedID, bootstrapID); err != nil {
		return err
	}
	updated, err := scanDNSPolicy(tx.QueryRowContext(ctx, dnsPolicySelectSQL+` where server_id=?`, v.ServerID))
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	*v = *updated
	return nil
}

// bindServerDNSGroupTx resolves one resolver group of a policy write to the
// list id it binds, and reports whether the content of the server's custom
// list changed (which invalidates that group's benchmark selection although the
// bound id stays the same). Candidates make the group custom; they may only be
// combined with list id 0 or the server's own custom list. Without candidates,
// list id 0 means no encrypted resolvers, the server's own custom list keeps
// it unchanged, and any other id must be an enabled shared list of that kind.
func bindServerDNSGroupTx(ctx context.Context, tx *sql.Tx, serverID int64, kind model.DNSListKind, listID int64, candidates []model.DNSCandidate, ts string) (int64, bool, error) {
	var listKind model.DNSListKind
	var enabled int
	var owner int64
	if listID > 0 {
		if err := tx.QueryRowContext(ctx, `select kind,enabled,coalesce(owner_server_id,0) from dns_lists where id=?`, listID).Scan(&listKind, &enabled, &owner); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return 0, false, fmt.Errorf("dns list %d does not exist", listID)
			}
			return 0, false, err
		}
		if listKind != kind {
			return 0, false, errors.New("dns policy list kinds do not match")
		}
		if owner != 0 && owner != serverID {
			return 0, false, errors.New("dns list holds another server's custom resolvers")
		}
	}
	if len(candidates) == 0 {
		switch {
		case listID < 0:
			return 0, false, errors.New("dns list id must not be negative")
		case listID == 0 && kind == model.DNSListEncrypted:
			return 0, false, nil
		case listID == 0:
			return 0, false, errors.New("a bootstrap dns list or custom bootstrap resolvers are required")
		case owner == serverID:
			return listID, false, nil
		case enabled == 0:
			return 0, false, errors.New("dns policy cannot select a disabled list")
		default:
			return listID, false, nil
		}
	}
	if listID > 0 && owner == 0 {
		return 0, false, errors.New("choose either a shared dns list or custom resolvers, not both")
	}
	list := core.ServerDNSCustomList(serverID, kind, core.ServerDNSCustomCandidates(candidates))
	if err := core.ValidateDNSList(list); err != nil {
		return 0, false, err
	}
	encoded, err := json.Marshal(list.Candidates)
	if err != nil {
		return 0, false, err
	}
	var id int64
	var stored string
	err = tx.QueryRowContext(ctx, `select id,candidates_json from dns_lists where owner_server_id=? and kind=?`, serverID, kind).Scan(&id, &stored)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		res, err := tx.ExecContext(ctx, `insert into dns_lists(name,kind,revision,candidates_json,enabled,protected,owner_server_id,created_at,updated_at) values(?,?,1,?,1,0,?,?,?)`, list.Name, kind, string(encoded), serverID, ts, ts)
		if err != nil {
			return 0, false, err
		}
		id, _ = res.LastInsertId()
		return id, true, nil
	case err != nil:
		return 0, false, err
	}
	if stored == string(encoded) {
		return id, false, nil
	}
	if _, err := tx.ExecContext(ctx, `update dns_lists set revision=revision+1,candidates_json=?,updated_at=? where id=?`, string(encoded), ts, id); err != nil {
		return 0, false, err
	}
	return id, true, nil
}
