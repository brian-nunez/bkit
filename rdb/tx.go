package rdb

import (
	"context"
	"database/sql"
	"errors"
)

// Tx is a transaction started by DB.Tx. It implements Querier and is valid
// only inside the function passed to DB.Tx. Like *sql.Tx, it is not meant
// for concurrent queries.
type Tx struct {
	session
	tx *sql.Tx
}

// SQL returns the underlying transaction, for libraries that need a
// *sql.Tx. Queries run through it are not rebound and their errors not
// classified. Do not commit or roll it back.
func (t *Tx) SQL() *sql.Tx {
	return t.tx
}

// Tx runs fn in a transaction. It commits when fn returns nil and rolls back
// when fn returns an error or panics; a panic continues after the rollback.
// The returned error is fn's error, joined with any rollback error, or the
// commit error. Tx does not retry; a failure matching ErrRetryable may
// succeed if the caller runs Tx again.
func (db *DB) Tx(ctx context.Context, fn func(tx *Tx) error, opts ...TxOption) (err error) {
	var o sql.TxOptions
	for _, opt := range opts {
		opt(&o)
	}
	stx, err := db.db.BeginTx(ctx, &o)
	if err != nil {
		return db.wrap("begin", err)
	}
	t := &Tx{session: db.session, tx: stx}
	t.run = stx

	committed := false
	defer func() {
		if !committed {
			// ErrTxDone means database/sql already rolled back, as it does
			// when ctx ends.
			rbErr := stx.Rollback()
			if rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) && err != nil {
				err = errors.Join(err, db.wrap("rollback", rbErr))
			}
		}
	}()
	if err := fn(t); err != nil {
		return err
	}
	committed = true
	return db.wrap("commit", stx.Commit())
}
