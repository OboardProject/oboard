package store

import (
	"context"
	"database/sql"
	"sync/atomic"
)

// countingDB tracks top-level Store operations. Health-report transactions use
// countingTx below so their statements are included without replacing the
// registered SQLite driver.
type countingDB struct {
	db    *sql.DB
	stmts atomic.Int64
	txs   atomic.Int64
}

func newCountingDB(db *sql.DB) *countingDB { return &countingDB{db: db} }

func (d *countingDB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	d.stmts.Add(1)
	return d.db.ExecContext(ctx, query, args...)
}

func (d *countingDB) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	d.stmts.Add(1)
	return d.db.QueryContext(ctx, query, args...)
}

func (d *countingDB) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	d.stmts.Add(1)
	return d.db.QueryRowContext(ctx, query, args...)
}

func (d *countingDB) BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error) {
	d.stmts.Add(1)
	d.txs.Add(1)
	return d.db.BeginTx(ctx, opts)
}

func (d *countingDB) BeginCountedTx(ctx context.Context, opts *sql.TxOptions) (*countingTx, error) {
	tx, err := d.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &countingTx{Tx: tx, statements: &d.stmts}, nil
}

func (d *countingDB) Begin() (*sql.Tx, error) { return d.BeginTx(context.Background(), nil) }

func (d *countingDB) Exec(query string, args ...any) (sql.Result, error) {
	d.stmts.Add(1)
	return d.db.Exec(query, args...)
}

func (d *countingDB) Query(query string, args ...any) (*sql.Rows, error) {
	d.stmts.Add(1)
	return d.db.Query(query, args...)
}

func (d *countingDB) QueryRow(query string, args ...any) *sql.Row {
	d.stmts.Add(1)
	return d.db.QueryRow(query, args...)
}

func (d *countingDB) Conn(ctx context.Context) (*sql.Conn, error) {
	d.stmts.Add(1)
	return d.db.Conn(ctx)
}

func (d *countingDB) Stats() sql.DBStats { return d.db.Stats() }

func (d *countingDB) Close() error { return d.db.Close() }

type countingTx struct {
	*sql.Tx
	statements *atomic.Int64
}

func (tx *countingTx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	tx.statements.Add(1)
	return tx.Tx.ExecContext(ctx, query, args...)
}

func (tx *countingTx) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	tx.statements.Add(1)
	return tx.Tx.QueryRowContext(ctx, query, args...)
}
