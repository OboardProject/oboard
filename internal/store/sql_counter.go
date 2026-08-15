package store

import (
	"context"
	"database/sql"
	"sync/atomic"
)

// countingDB wraps *sql.DB so hot-path tests can assert that statement and
// write-transaction counts stay constant as the managed fleet grows. The
// counter is process-local and is never persisted.
type countingDB struct {
	db    *sql.DB
	stmts atomic.Int64
	txs   atomic.Int64
}

func newCountingDB(db *sql.DB) *countingDB {
	return &countingDB{db: db}
}

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

func (d *countingDB) Begin() (*sql.Tx, error) {
	return d.BeginTx(context.Background(), nil)
}

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
