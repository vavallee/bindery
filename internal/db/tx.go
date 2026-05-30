package db

import (
	"context"
	"database/sql"
)

// dbExecutor is the read+write subset of *sql.DB and *sql.Tx the repos need
// when they participate in a caller-supplied transaction. WithTx clones a
// repo with exec swapped from *sql.DB to *sql.Tx, so callers driving a
// multi-repo atomic operation (e.g. calibre.Rollback) can route every
// touched method through one transaction without each repo re-implementing
// "tx variant" copies of its public surface.
//
// Note: MaxOpenConns is pinned to 1 (see Open / OpenMemory). That means
// reads issued against the bare *sql.DB while a transaction is open would
// deadlock waiting for the only pooled connection. WithTx is the only safe
// way to mix reads and writes inside a rollback-style multi-repo tx.
type dbExecutor interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}
