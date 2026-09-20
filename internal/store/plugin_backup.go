package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/OboardProject/oboard/internal/security"
)

func rewrapPluginSecrets(ctx context.Context, tx *sql.Tx, sourceSecret, targetSecret string) error {
	type secretRow struct {
		pluginID        int64
		name, encrypted string
	}
	rows, err := tx.QueryContext(ctx, `select plugin_id,name,value_encrypted from plugin_installation_secrets where value_encrypted<>''`)
	if err != nil {
		return err
	}
	var items []secretRow
	for rows.Next() {
		var item secretRow
		if err := rows.Scan(&item.pluginID, &item.name, &item.encrypted); err != nil {
			rows.Close()
			return err
		}
		items = append(items, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, item := range items {
		plain, err := security.DecryptSecret(sourceSecret, "plugin-secret", item.encrypted)
		if err != nil {
			return fmt.Errorf("restore plugin secret: %w", err)
		}
		encrypted, err := security.EncryptSecret(targetSecret, "plugin-secret", plain)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `update plugin_installation_secrets set value_encrypted=?,updated_at=? where plugin_id=? and name=?`, encrypted, now(), item.pluginID, item.name); err != nil {
			return err
		}
	}
	return nil
}
