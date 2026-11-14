package rdb

import (
	"context"
	"database/sql"
	"errors"
)

// runner is the query surface shared by *sql.DB and *sql.Tx.
type runner interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// session runs queries for a DB, a Tx, or a raw view of either. It
// implements Querier.
type session struct {
	run     runner
	driver  Driver
	dialect Dialect
	rebind  bool
	strict  bool
}

// Exec runs a query that returns no rows. Failures are *Error values
// classified by the driver.
func (s session) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	res, err := s.run.ExecContext(ctx, s.prepare(query), args...)
	if err != nil {
		return nil, s.wrap("exec", err)
	}
	return res, nil
}

// Query runs a query and returns its rows, which the caller must close.
// Failures are *Error values classified by the driver; errors from the rows
// themselves are not wrapped.
func (s session) Query(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	rows, err := s.run.QueryContext(ctx, s.prepare(query), args...)
	if err != nil {
		return nil, s.wrap("query", err)
	}
	return rows, nil
}

// Dialect returns the dialect of the database.
func (s session) Dialect() Dialect {
	return s.dialect
}

// Raw returns a Querier that sends queries exactly as written, without
// rebinding placeholders. Scanning and error classification still apply.
func (s session) Raw() Querier {
	s.rebind = false
	return s
}

// strictScan reports whether scanning rejects unmatched columns.
func (s session) strictScan() bool {
	return s.strict
}

// prepare rebinds query when the session rebinds for its dialect.
func (s session) prepare(query string) string {
	if s.rebind && s.dialect == Postgres {
		return rebindDollar(query)
	}
	return query
}

// wrap returns err as an *Error for op, classified by the driver. It
// returns nil for nil and leaves an *Error unchanged.
func (s session) wrap(op string, err error) error {
	if err == nil {
		return nil
	}
	if e, ok := errors.AsType[*Error](err); ok {
		return e
	}
	return &Error{Op: op, Kind: s.driver.Classify(err), Err: err}
}
