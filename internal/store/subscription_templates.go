package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
)

var ErrSubscriptionTemplateConflict = errors.New("subscription template revision conflict")

func (s *Store) ListSubscriptionClientTemplates(ctx context.Context) ([]model.SubscriptionClientTemplate, error) {
	overrides, err := s.listSubscriptionTemplateOverrides(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]model.SubscriptionClientTemplate, 0, len(core.ConcreteSubscriptionFormats()))
	for _, format := range core.ConcreteSubscriptionFormats() {
		item, err := s.composeSubscriptionClientTemplate(format, overrides[format])
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, nil
}

func (s *Store) GetSubscriptionClientTemplate(ctx context.Context, format model.SubscriptionFormat) (model.SubscriptionClientTemplate, error) {
	format = core.NormalizeSubscriptionFormatForAPI(format)
	if !core.IsConcreteSubscriptionFormat(format) {
		return model.SubscriptionClientTemplate{}, fmt.Errorf("unsupported subscription format %q", format)
	}
	overrides, err := s.listSubscriptionTemplateOverrides(ctx)
	if err != nil {
		return model.SubscriptionClientTemplate{}, err
	}
	return s.composeSubscriptionClientTemplate(format, overrides[format])
}

func (s *Store) EffectiveSubscriptionTemplateContent(ctx context.Context, format model.SubscriptionFormat) (string, string, error) {
	item, err := s.GetSubscriptionClientTemplate(ctx, format)
	if err != nil {
		return "", "", err
	}
	return item.Content, core.MustSubscriptionTemplateDigest(item.Content), nil
}

func (s *Store) PutSubscriptionClientTemplate(ctx context.Context, format model.SubscriptionFormat, content string, expectedRevision int64, updatedBy int64) (model.SubscriptionClientTemplate, error) {
	format = core.NormalizeSubscriptionFormatForAPI(format)
	if err := core.ValidateSubscriptionTemplateWithPreview(format, content); err != nil {
		return model.SubscriptionClientTemplate{}, err
	}
	current, err := s.GetSubscriptionClientTemplate(ctx, format)
	if err != nil {
		return model.SubscriptionClientTemplate{}, err
	}
	if current.Revision != expectedRevision {
		return model.SubscriptionClientTemplate{}, ErrSubscriptionTemplateConflict
	}
	ts := now()
	builtinDigest, err := core.BuiltinSubscriptionTemplateDigest(format)
	if err != nil {
		return model.SubscriptionClientTemplate{}, err
	}
	baseDigest := current.BaseBuiltinDigest
	if current.Source != "custom" {
		baseDigest = builtinDigest
	}
	if current.Source != "custom" {
		if _, err := s.db.ExecContext(ctx, `insert into subscription_client_templates(format,content,revision,base_builtin_digest,updated_by,updated_at) values(?,?,1,?,?,?)`,
			string(format), content, baseDigest, updatedBy, ts); err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "unique") {
				return model.SubscriptionClientTemplate{}, ErrSubscriptionTemplateConflict
			}
			return model.SubscriptionClientTemplate{}, err
		}
	} else {
		res, err := s.db.ExecContext(ctx, `update subscription_client_templates set content=?, revision=revision+1, updated_by=?, updated_at=? where format=? and revision=?`,
			content, updatedBy, ts, string(format), expectedRevision)
		if err != nil {
			return model.SubscriptionClientTemplate{}, err
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return model.SubscriptionClientTemplate{}, ErrSubscriptionTemplateConflict
		}
	}
	return s.GetSubscriptionClientTemplate(ctx, format)
}

func (s *Store) ResetSubscriptionClientTemplate(ctx context.Context, format model.SubscriptionFormat) (model.SubscriptionClientTemplate, error) {
	format = core.NormalizeSubscriptionFormatForAPI(format)
	if !core.IsConcreteSubscriptionFormat(format) {
		return model.SubscriptionClientTemplate{}, fmt.Errorf("unsupported subscription format %q", format)
	}
	if _, err := s.db.ExecContext(ctx, `delete from subscription_client_templates where format=?`, string(format)); err != nil {
		return model.SubscriptionClientTemplate{}, err
	}
	return s.GetSubscriptionClientTemplate(ctx, format)
}

func (s *Store) listSubscriptionTemplateOverrides(ctx context.Context) (map[model.SubscriptionFormat]model.SubscriptionClientTemplate, error) {
	rows, err := s.db.QueryContext(ctx, `select format,content,revision,base_builtin_digest,updated_by,updated_at from subscription_client_templates`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[model.SubscriptionFormat]model.SubscriptionClientTemplate{}
	for rows.Next() {
		var item model.SubscriptionClientTemplate
		var format, updatedAt string
		var updatedBy sql.NullInt64
		if err := rows.Scan(&format, &item.Content, &item.Revision, &item.BaseBuiltinDigest, &updatedBy, &updatedAt); err != nil {
			return nil, err
		}
		item.Format = model.SubscriptionFormat(format)
		item.Source = "custom"
		ts := parseTime(updatedAt)
		item.UpdatedAt = &ts
		if updatedBy.Valid {
			id := updatedBy.Int64
			item.UpdatedBy = &id
		}
		out[item.Format] = item
	}
	return out, rows.Err()
}

func (s *Store) composeSubscriptionClientTemplate(format model.SubscriptionFormat, override model.SubscriptionClientTemplate) (model.SubscriptionClientTemplate, error) {
	builtin, err := core.BuiltinSubscriptionTemplate(format)
	if err != nil {
		return model.SubscriptionClientTemplate{}, err
	}
	builtinDigest := core.MustSubscriptionTemplateDigest(builtin)
	item := model.SubscriptionClientTemplate{
		Format:        format,
		Label:         core.SubscriptionFormatLabel(format),
		Content:       builtin,
		Source:        "builtin",
		BuiltinDigest: builtinDigest,
		Markers:       core.SubscriptionTemplateMarkerNames(format),
	}
	if override.Format == format && strings.TrimSpace(override.Content) != "" {
		item.Content = override.Content
		item.Source = "custom"
		item.Revision = override.Revision
		item.BaseBuiltinDigest = override.BaseBuiltinDigest
		item.BuiltinUpdated = override.BaseBuiltinDigest != "" && override.BaseBuiltinDigest != builtinDigest
		item.UpdatedBy = override.UpdatedBy
		item.UpdatedAt = override.UpdatedAt
	}
	return item, nil
}
