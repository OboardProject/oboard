package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// withForeignKeysDisabled runs fn on one dedicated connection with foreign-key
// enforcement disabled and restores it before that connection returns to the
// pool.
//
// PRAGMA foreign_keys is per connection and is a no-op inside a transaction, so
// a table rebuild has to own the connection it runs on. Executing the PRAGMA
// against the pool instead is wrong twice over: the rebuild transaction can
// open on a different connection and still be enforced, and the restoring
// PRAGMA can land on a third connection, leaving an idle pooled connection with
// enforcement off for the rest of the process. Every later delete served by
// that connection would then skip ON DELETE CASCADE and leave rows pointing at
// a deleted parent.
//
// A failed restore fails the caller: these rebuilds run during startup
// migration, so refusing to continue is safer than handing an unenforced
// connection back to the pool.
func (s *Store) withForeignKeysDisabled(ctx context.Context, fn func(context.Context, *sql.Conn) error) (err error) {
	conn, connErr := s.db.Conn(ctx)
	if connErr != nil {
		return connErr
	}
	defer func() {
		// The restore must run even when ctx is already done, otherwise the
		// connection goes back to the pool unenforced.
		_, restoreErr := conn.ExecContext(context.WithoutCancel(ctx), `pragma foreign_keys=on`)
		if restoreErr != nil {
			restoreErr = fmt.Errorf("restore foreign key enforcement: %w", restoreErr)
		}
		err = errors.Join(err, restoreErr, conn.Close())
	}()
	if _, err := conn.ExecContext(ctx, `pragma foreign_keys=off`); err != nil {
		return err
	}
	return fn(ctx, conn)
}
