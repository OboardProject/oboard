package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/OboardProject/oboard/internal/auditrisk"
)

const accountAuditPolicySetting = "account_audit_policy_v1"

type AuditResourceThreshold struct {
	Start uint64 `json:"start"`
	Full  uint64 `json:"full"`
	Unit  string `json:"unit"`
}
type AuditResourceThresholds struct {
	RequestRate *AuditResourceThreshold `json:"request_rate"`
	Connections *AuditResourceThreshold `json:"connections"`
	TrafficRate *AuditResourceThreshold `json:"traffic_rate"`
}
type AccountAuditSourceConfig struct {
	IPv4Bits int   `json:"ipv4_prefix_bits"`
	IPv6Bits int   `json:"ipv6_prefix_bits"`
	Epoch    int64 `json:"epoch"`
}

type AccountAuditPolicy struct {
	SourceGrouping AccountAuditSourceConfig `json:"source_grouping"`
	Revision       int64                    `json:"revision"`
	Policy         auditrisk.Policy         `json:"policy"`
	Resources      AuditResourceThresholds  `json:"resources"`
}

func accountAuditPolicyVersion(revision int64) string {
	return fmt.Sprintf("account-risk-policy-v1:%d", revision)
}
func decodeAccountAuditPolicy(raw string) (AccountAuditPolicy, error) {
	c := AccountAuditPolicy{Policy: auditrisk.DefaultPolicy(), SourceGrouping: AccountAuditSourceConfig{IPv4Bits: 24, IPv6Bits: 56, Epoch: 1}}
	c.Policy.Version = accountAuditPolicyVersion(0)
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &c); err != nil {
			return c, err
		}
	}
	return c, ValidateAccountAuditPolicy(c)
}
func ValidateAccountAuditPolicy(c AccountAuditPolicy) error {
	if c.SourceGrouping.IPv4Bits < 0 || c.SourceGrouping.IPv4Bits > 24 || c.SourceGrouping.IPv6Bits < 0 || c.SourceGrouping.IPv6Bits > 56 || c.SourceGrouping.Epoch < 1 || c.SourceGrouping.Epoch > 9007199254740991 {
		return errors.New("source grouping requires IPv4 /0..24, IPv6 /0..56 and a positive safe-integer epoch")
	}
	if c.Revision < 0 || c.Revision == math.MaxInt64 || c.Policy.Version != accountAuditPolicyVersion(c.Revision) || c.Policy.SourceCapacity != 32 || c.Policy.MinimumBytes > 1<<40 {
		return errors.New("invalid account policy revision, version, capacity or byte threshold")
	}
	if err := c.Policy.Validate(); err != nil {
		return err
	}
	for unit, threshold := range map[string]*AuditResourceThreshold{"requests/second": c.Resources.RequestRate, "connections": c.Resources.Connections, "bytes/second": c.Resources.TrafficRate} {
		if threshold != nil && (threshold.Unit != unit || threshold.Start >= threshold.Full) {
			return errors.New("invalid resource observation threshold or unit")
		}
	}
	return nil
}
func (s *Store) GetAccountAuditPolicy(ctx context.Context) (AccountAuditPolicy, error) {
	raw, err := s.GetSetting(ctx, accountAuditPolicySetting)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return AccountAuditPolicy{}, err
	}
	return decodeAccountAuditPolicy(raw)
}
func (s *Store) SetAccountAuditPolicy(ctx context.Context, c AccountAuditPolicy, at time.Time) (AccountAuditPolicy, error) {
	if err := ValidateAccountAuditPolicy(c); err != nil {
		return c, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return c, err
	}
	defer tx.Rollback()
	var raw string
	err = tx.QueryRowContext(ctx, `select value from app_settings where key=?`, accountAuditPolicySetting).Scan(&raw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return c, err
	}
	old, err := decodeAccountAuditPolicy(raw)
	if err != nil {
		return c, err
	}
	if (c.SourceGrouping.IPv4Bits != old.SourceGrouping.IPv4Bits || c.SourceGrouping.IPv6Bits != old.SourceGrouping.IPv6Bits) && c.SourceGrouping.Epoch <= old.SourceGrouping.Epoch {
		return c, errors.New("changing source prefix lengths requires a new epoch")
	}
	if c.SourceGrouping.Epoch < old.SourceGrouping.Epoch {
		return c, errors.New("source grouping epoch cannot decrease")
	}
	if old.Revision != c.Revision {
		return c, errors.New("account policy revision conflict")
	}
	c.Revision++
	c.Policy.Version = accountAuditPolicyVersion(c.Revision)
	encoded, err := json.Marshal(c)
	if err != nil {
		return c, err
	}
	_, err = tx.ExecContext(ctx, `insert into app_settings(key,value,updated_at) values(?,?,?) on conflict(key) do update set value=excluded.value,updated_at=excluded.updated_at`, accountAuditPolicySetting, string(encoded), at.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return c, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO account_audit_dirty(user_id,revision) SELECT user_id,revision+1 FROM account_audit_snapshots WHERE 1 ON CONFLICT(user_id) DO UPDATE SET revision=MAX(account_audit_dirty.revision,excluded.revision)+1`)
	if err != nil {
		return c, err
	}
	if err := tx.Commit(); err != nil {
		return c, err
	}
	s.settingsRevision.Add(1)
	return c, nil
}
