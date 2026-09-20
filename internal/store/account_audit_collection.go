package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

const auditCollectionSetting = "account_audit_collection"

func decodeAuditCollection(raw string) (model.AuditCollectionConfig, error) {
	c := model.AuditCollectionConfig{Mode: "light", Diagnostics: []model.AuditDiagnostic{}}
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &c); err != nil {
			return c, err
		}
	}
	return c, nil
}
func (s *Store) AuditCollection(ctx context.Context) (model.AuditCollectionConfig, error) {
	raw, err := s.GetSetting(ctx, auditCollectionSetting)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	if err != nil {
		return model.AuditCollectionConfig{}, err
	}
	return decodeAuditCollection(raw)
}
func ValidateAuditCollection(c model.AuditCollectionConfig, at time.Time) error {
	if c.Mode != "light" && c.Mode != "standard" {
		return errors.New("collection mode must be light or standard")
	}
	if c.Revision < 0 || len(c.Diagnostics) > model.AuditDiagnosticLimit {
		return errors.New("invalid revision or diagnostic capacity exceeded")
	}
	seen := map[string]bool{}
	for _, d := range c.Diagnostics {
		if (d.Scope != "user" && d.Scope != "node") || d.ID <= 0 || !d.Until.After(at) || d.Until.After(at.Add(time.Hour)) {
			return errors.New("diagnostic requires user/node and an expiry within one hour")
		}
		key := fmt.Sprintf("%s:%d", d.Scope, d.ID)
		if seen[key] {
			return errors.New("duplicate diagnostic scope")
		}
		seen[key] = true
	}
	return nil
}

// SetAuditCollection replaces the bounded configuration using optimistic concurrency.
// Absolute expiry is supplied by the caller so retries cannot extend a lease.
func (s *Store) SetAuditCollection(ctx context.Context, c model.AuditCollectionConfig, at time.Time) (model.AuditCollectionConfig, error) {
	if err := ValidateAuditCollection(c, at); err != nil {
		return c, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return c, err
	}
	defer tx.Rollback()
	var raw string
	err = tx.QueryRowContext(ctx, `select value from app_settings where key=?`, auditCollectionSetting).Scan(&raw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return c, err
	}
	old, err := decodeAuditCollection(raw)
	if err != nil {
		return c, err
	}
	if old.Revision != c.Revision {
		return c, errors.New("collection revision conflict")
	}
	for _, d := range c.Diagnostics {
		table := "users"
		if d.Scope == "node" {
			table = "servers"
		}
		var exists int
		if err := tx.QueryRowContext(ctx, "select 1 from "+table+" where id=?", d.ID).Scan(&exists); err != nil {
			return c, errors.New("diagnostic target not found")
		}
	}
	c.Revision++
	encoded, err := json.Marshal(c)
	if err != nil {
		return c, err
	}
	_, err = tx.ExecContext(ctx, `insert into app_settings(key,value,updated_at) values(?,?,?) on conflict(key) do update set value=excluded.value,updated_at=excluded.updated_at`, auditCollectionSetting, string(encoded), at.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return c, err
	}
	return c, tx.Commit()
}

// AuditDetailAllowedTx enforces global and per-account row budgets inside the
// insertion transaction. The base activity stream has an independent budget.
func AuditDetailAllowedTx(ctx context.Context, tx *sql.Tx, serverID, userID int64, at time.Time) (bool, error) {
	budget, err := newAuditDetailBudget(ctx, tx, at)
	if err != nil {
		return false, err
	}
	return budget.allowed(ctx, tx, serverID, userID)
}

type auditDetailBudget struct {
	standard        bool
	nodes, users    map[int64]bool
	globalLoaded    bool
	globalRemaining int
	userRemaining   map[int64]int
}

func newAuditDetailBudget(ctx context.Context, tx *sql.Tx, at time.Time) (*auditDetailBudget, error) {
	var raw string
	err := tx.QueryRowContext(ctx, `select value from app_settings where key=?`, auditCollectionSetting).Scan(&raw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	c, err := decodeAuditCollection(raw)
	if err != nil {
		return nil, err
	}
	b := &auditDetailBudget{standard: c.Mode == "standard", nodes: map[int64]bool{}, users: map[int64]bool{}, userRemaining: map[int64]int{}}
	for _, d := range c.Diagnostics {
		if !d.Until.After(at) {
			continue
		}
		switch d.Scope {
		case "node":
			b.nodes[d.ID] = true
		case "user":
			b.users[d.ID] = true
		}
	}
	return b, nil
}

func (b *auditDetailBudget) allowed(ctx context.Context, tx *sql.Tx, serverID, userID int64) (bool, error) {
	if !b.standard && !b.nodes[serverID] && !b.users[userID] {
		return false, nil
	}
	if !b.globalLoaded {
		var count int
		if err := tx.QueryRowContext(ctx, `select count(*) from (select 1 from connection_audit_reports limit ?)`, model.AuditDetailGlobalLimit).Scan(&count); err != nil {
			return false, err
		}
		b.globalRemaining = model.AuditDetailGlobalLimit - count
		b.globalLoaded = true
	}
	if b.globalRemaining == 0 {
		return false, nil
	}
	remaining, loaded := b.userRemaining[userID]
	if !loaded {
		var count int
		if err := tx.QueryRowContext(ctx, `select count(*) from (select 1 from connection_audit_reports where user_id=? limit ?)`, userID, model.AuditDetailUserLimit).Scan(&count); err != nil {
			return false, err
		}
		remaining = model.AuditDetailUserLimit - count
		b.userRemaining[userID] = remaining
	}
	return remaining > 0, nil
}

func (b *auditDetailBudget) inserted(userID int64) {
	b.globalRemaining--
	b.userRemaining[userID]--
}
