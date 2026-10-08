package store

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/OboardProject/oboard/internal/model"
	"time"
)

func runtimeSecurityKey(id int64, kind string) string {
	return fmt.Sprintf("server_runtime_security.%d.%s", id, kind)
}
func (s *Store) RuntimeSecurityDesired(ctx context.Context, id int64) (model.RuntimeSecurityRequest, error) {
	value := model.RuntimeSecurityRequest{Mode: "standard"}
	raw, err := s.GetSetting(ctx, runtimeSecurityKey(id, "desired"))
	if err != nil {
		return value, err
	}
	if raw != "" {
		err = json.Unmarshal([]byte(raw), &value)
	}
	return value, err
}
func (s *Store) SaveRuntimeSecurityDesired(ctx context.Context, id int64, value model.RuntimeSecurityRequest) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return s.SetSetting(ctx, runtimeSecurityKey(id, "desired"), string(raw))
}
func (s *Store) RuntimeSecurityReport(ctx context.Context, id int64) (*model.RuntimeSecurityReport, error) {
	raw, err := s.GetSetting(ctx, runtimeSecurityKey(id, "report"))
	if err != nil || raw == "" {
		return nil, err
	}
	var value model.RuntimeSecurityReport
	err = json.Unmarshal([]byte(raw), &value)
	return &value, err
}
func (s *Store) SaveRuntimeSecurityReport(ctx context.Context, id int64, value model.RuntimeSecurityReport) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `insert into app_settings(key,value,updated_at) values(?,?,?) on conflict(key) do update set value=excluded.value, updated_at=excluded.updated_at where app_settings.value<>excluded.value`, runtimeSecurityKey(id, "report"), string(raw), time.Now().UTC().Format(time.RFC3339Nano))
	return err
}
