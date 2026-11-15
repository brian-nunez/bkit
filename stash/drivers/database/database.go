package database

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"math"
	"regexp"
	"strconv"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/brian-nunez/bkit/rdb"
	"github.com/brian-nunez/bkit/stash"
)

const (
	// defaultTable is the table New uses without WithTable.
	defaultTable = "stash"

	// keysPage is how many keys Keys reads per query.
	keysPage = 500

	// sweepEvery is how many writes pass between sweeps of expired rows.
	sweepEvery = 100

	// incrAttempts is how many times Incr runs its transaction when the
	// database reports a conflict.
	incrAttempts = 10
)

// validTable matches the table names WithTable accepts.
var validTable = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,62}$`)

// Option configures New.
type Option func(*options)

type options struct {
	table string
}

// WithTable stores keys in table instead of "stash". The name is letters,
// digits, and underscores, starting with a letter or underscore, at most 63
// characters.
func WithTable(table string) Option {
	return func(o *options) { o.table = table }
}

// New returns a store that keeps keys in a table of db, creating the table
// and its index when they are missing. db is an SQLite, Postgres, or MySQL
// database opened with rdb; the store shares its connection pool. Closing
// the store leaves db open.
func New(ctx context.Context, db *rdb.DB, opts ...Option) (*stash.Store, error) {
	b, err := open(ctx, db, opts...)
	if err != nil {
		return nil, err
	}
	return stash.New(b), nil
}

// open creates the table and returns the backend New wraps.
func open(ctx context.Context, db *rdb.DB, opts ...Option) (*backend, error) {
	o := options{table: defaultTable}
	for _, opt := range opts {
		opt(&o)
	}
	if !validTable.MatchString(o.table) {
		return nil, &stash.Error{Op: "open", Err: fmt.Errorf("%w: invalid table name", stash.ErrInvalidOption)}
	}
	q, err := queriesFor(db.Dialect(), o.table)
	if err != nil {
		return nil, &stash.Error{Op: "open", Err: err}
	}
	for _, stmt := range q.create {
		if _, err := db.Exec(ctx, stmt); err != nil {
			return nil, &stash.Error{Op: "open", Err: err}
		}
	}
	b := &backend{db: db, q: q, now: time.Now}
	b.tx = func(ctx context.Context, fn func(*rdb.Tx) error) error { return db.Tx(ctx, fn) }
	return b, nil
}

// queries holds the SQL for one dialect. Every query uses ? placeholders,
// which rdb rewrites for the dialect.
type queries struct {
	create []string

	get, exists, ttl string
	upsert           string
	ifAbsent         string // stores the row unless a live one exists
	ifAbsentNows     int    // how many trailing "now" arguments ifAbsent takes
	del              string
	expire           string
	lockFirst        string // takes the write lock before the read, for SQLite
	selectRow        string // reads the row inside the Incr transaction
	insert, update   string
	keys             string
	deletePrefix     string
	sweep            string

	// prefixLen returns the length the prefix expression compares: runes
	// for text keys, bytes for binary keys.
	prefixLen func(string) int
}

// queriesFor returns the SQL for dialect, with keys in table t.
func queriesFor(dialect rdb.Dialect, t string) (queries, error) {
	const live = "(expires_at IS NULL OR expires_at > ?)"
	var q queries
	prefix := "substr(k, 1, ?) = ?"
	q.prefixLen = utf8.RuneCountInString
	switch dialect {
	case rdb.SQLite:
		q.create = []string{
			"CREATE TABLE IF NOT EXISTS " + t + " (k TEXT PRIMARY KEY, v BLOB NOT NULL, expires_at INTEGER)",
			"CREATE INDEX IF NOT EXISTS " + t + "_expires_at ON " + t + " (expires_at)",
		}
		q.lockFirst = "UPDATE " + t + " SET k = k WHERE k = ?"
		q.selectRow = "SELECT v, expires_at FROM " + t + " WHERE k = ?"
	case rdb.Postgres:
		q.create = []string{
			"CREATE TABLE IF NOT EXISTS " + t + ` (k TEXT COLLATE "C" PRIMARY KEY, v BYTEA NOT NULL, expires_at BIGINT)`,
			"CREATE INDEX IF NOT EXISTS " + t + "_expires_at ON " + t + " (expires_at)",
		}
		prefix = "left(k, ?) = ?"
		q.selectRow = "SELECT v, expires_at FROM " + t + " WHERE k = ? FOR UPDATE"
	case rdb.MySQL:
		q.create = []string{
			"CREATE TABLE IF NOT EXISTS " + t + " (k VARBINARY(512) PRIMARY KEY, v LONGBLOB NOT NULL, expires_at BIGINT NULL, " +
				"INDEX " + t + "_expires_at (expires_at)) ENGINE=InnoDB",
		}
		prefix = "LEFT(k, ?) = ?"
		q.prefixLen = func(s string) int { return len(s) }
		q.selectRow = "SELECT v, expires_at FROM " + t + " WHERE k = ? FOR UPDATE"
	default:
		return q, fmt.Errorf("%w: unsupported dialect %q", stash.ErrInvalidOption, dialect)
	}

	q.get = "SELECT v FROM " + t + " WHERE k = ? AND " + live
	q.exists = "SELECT COUNT(*) FROM " + t + " WHERE k = ? AND " + live
	q.ttl = "SELECT expires_at FROM " + t + " WHERE k = ? AND " + live
	q.del = "DELETE FROM " + t + " WHERE k = ? AND " + live
	q.expire = "UPDATE " + t + " SET expires_at = ? WHERE k = ? AND " + live
	q.insert = "INSERT INTO " + t + " (k, v, expires_at) VALUES (?, ?, ?)"
	q.update = "UPDATE " + t + " SET v = ?, expires_at = ? WHERE k = ?"
	q.keys = "SELECT k FROM " + t + " WHERE " + prefix + " AND k > ? AND " + live + " ORDER BY k LIMIT " + strconv.Itoa(keysPage)
	q.deletePrefix = "DELETE FROM " + t + " WHERE " + prefix
	q.sweep = "DELETE FROM " + t + " WHERE expires_at IS NOT NULL AND expires_at <= ?"
	if dialect == rdb.MySQL {
		q.upsert = q.insert + " ON DUPLICATE KEY UPDATE v = VALUES(v), expires_at = VALUES(expires_at)"
		// The IF conditions read expires_at before it is assigned, so both
		// see the existing row.
		q.ifAbsent = q.insert + " ON DUPLICATE KEY UPDATE" +
			" v = IF(expires_at IS NOT NULL AND expires_at <= ?, VALUES(v), v)," +
			" expires_at = IF(expires_at IS NOT NULL AND expires_at <= ?, VALUES(expires_at), expires_at)"
		q.ifAbsentNows = 2
	} else {
		q.upsert = q.insert + " ON CONFLICT (k) DO UPDATE SET v = excluded.v, expires_at = excluded.expires_at"
		q.ifAbsent = q.upsert + " WHERE " + t + ".expires_at IS NOT NULL AND " + t + ".expires_at <= ?"
		q.ifAbsentNows = 1
	}
	return q, nil
}

// backend implements stash.Backend over an rdb database.
type backend struct {
	db     *rdb.DB
	q      queries
	now    func() time.Time
	writes atomic.Uint64

	// tx runs fn in a transaction. Tests replace it to inject conflicts.
	tx func(ctx context.Context, fn func(*rdb.Tx) error) error
}

// ms returns now in Unix milliseconds, the unit of expires_at.
func (b *backend) ms() int64 {
	return b.now().UnixMilli()
}

// expiry returns the expires_at value for ttl from now: nil for no expiry.
func (b *backend) expiry(ttl time.Duration) any {
	if ttl <= 0 {
		return nil
	}
	return b.ms() + ttl.Milliseconds()
}

// wrote counts a write and sweeps expired rows every sweepEvery writes.
// Sweep failures are ignored: the next sweep retries, and reads never return
// expired rows.
func (b *backend) wrote(ctx context.Context) {
	if b.writes.Add(1)%sweepEvery == 0 {
		_, _ = b.db.Exec(ctx, b.q.sweep, b.ms())
	}
}

// affected returns the rows a statement changed. The supported drivers
// always report it, so a reporting error counts as no rows.
func affected(res interface{ RowsAffected() (int64, error) }) int64 {
	n, _ := res.RowsAffected()
	return n
}

// notFound maps rdb.ErrNotFound to stash.ErrNotFound.
func notFound(err error) error {
	if errors.Is(err, rdb.ErrNotFound) {
		return stash.ErrNotFound
	}
	return err
}

// Get returns the value of key.
func (b *backend) Get(ctx context.Context, key string) ([]byte, error) {
	v, err := rdb.One[[]byte](ctx, b.db, b.q.get, key, b.ms())
	return v, notFound(err)
}

// Set stores value under key.
func (b *backend) Set(ctx context.Context, key string, value []byte, opts stash.SetOptions) error {
	if value == nil {
		value = []byte{} // v is NOT NULL
	}
	defer b.wrote(ctx)
	if !opts.IfNotExists {
		_, err := b.db.Exec(ctx, b.q.upsert, key, value, b.expiry(opts.TTL))
		return err
	}
	args := []any{key, value, b.expiry(opts.TTL)}
	for range b.q.ifAbsentNows {
		args = append(args, b.ms())
	}
	res, err := b.db.Exec(ctx, b.q.ifAbsent, args...)
	if err != nil {
		return err
	}
	if affected(res) == 0 {
		return stash.ErrExists
	}
	return nil
}

// Delete removes key.
func (b *backend) Delete(ctx context.Context, key string) (bool, error) {
	res, err := b.db.Exec(ctx, b.q.del, key, b.ms())
	if err != nil {
		return false, err
	}
	return affected(res) > 0, nil
}

// Exists reports whether key is set.
func (b *backend) Exists(ctx context.Context, key string) (bool, error) {
	n, err := rdb.One[int64](ctx, b.db, b.q.exists, key, b.ms())
	return n > 0, err
}

// row is a stored row as Incr reads it.
type row struct {
	V         []byte `db:"v"`
	ExpiresAt *int64 `db:"expires_at"`
}

// Incr adds delta to the counter under key, in a transaction that locks the
// row. Two transactions that both create a missing key conflict, so the
// loser runs again, up to incrAttempts times.
func (b *backend) Incr(ctx context.Context, key string, delta int64) (int64, error) {
	defer b.wrote(ctx)
	for attempt := 1; ; attempt++ {
		var n int64
		err := b.tx(ctx, func(tx *rdb.Tx) error {
			v, e := b.incr(ctx, tx, key, delta)
			n = v
			return e
		})
		if err == nil {
			return n, nil
		}
		conflict := errors.Is(err, rdb.ErrRetryable) || errors.Is(err, rdb.ErrUniqueViolation)
		if !conflict || attempt == incrAttempts {
			return 0, err
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(time.Duration(attempt) * time.Millisecond):
		}
	}
}

// incr runs one increment inside tx.
func (b *backend) incr(ctx context.Context, tx *rdb.Tx, key string, delta int64) (int64, error) {
	if b.q.lockFirst != "" {
		if _, err := tx.Exec(ctx, b.q.lockFirst, key); err != nil {
			return 0, err
		}
	}
	r, err := rdb.One[row](ctx, tx, b.q.selectRow, key)
	found := err == nil
	if err != nil && !errors.Is(err, rdb.ErrNotFound) {
		return 0, err
	}
	var n int64
	expires := r.ExpiresAt
	if found && expires != nil && *expires <= b.ms() {
		expires = nil // an expired key counts as missing
	} else if found {
		if n, err = strconv.ParseInt(string(r.V), 10, 64); err != nil {
			return 0, stash.ErrNotInteger
		}
	}
	if (delta > 0 && n > math.MaxInt64-delta) || (delta < 0 && n < math.MinInt64-delta) {
		return 0, stash.ErrNotInteger
	}
	n += delta
	v := []byte(strconv.FormatInt(n, 10))
	if found {
		_, err = tx.Exec(ctx, b.q.update, v, expires, key)
	} else {
		_, err = tx.Exec(ctx, b.q.insert, key, v, nil)
	}
	return n, err
}

// TTL returns the time left before key expires.
func (b *backend) TTL(ctx context.Context, key string) (time.Duration, error) {
	exp, err := rdb.One[*int64](ctx, b.db, b.q.ttl, key, b.ms())
	if err != nil {
		return 0, notFound(err)
	}
	if exp == nil {
		return stash.NoExpiry, nil
	}
	return time.Duration(*exp-b.ms()) * time.Millisecond, nil
}

// Expire sets the expiry of key.
func (b *backend) Expire(ctx context.Context, key string, ttl time.Duration) error {
	res, err := b.db.Exec(ctx, b.q.expire, b.expiry(ttl), key, b.ms())
	if err != nil {
		return err
	}
	if affected(res) > 0 {
		return nil
	}
	// MySQL reports no change when the expiry already had this value, so
	// zero rows does not prove the key is missing.
	found, err := b.Exists(ctx, key)
	if err == nil && !found {
		err = stash.ErrNotFound
	}
	return err
}

// Keys yields the live keys that start with prefix, a page at a time in key
// order.
func (b *backend) Keys(ctx context.Context, prefix string) iter.Seq2[string, error] {
	return func(yield func(string, error) bool) {
		after := ""
		for {
			page, err := rdb.All[string](ctx, b.db, b.q.keys, b.q.prefixLen(prefix), prefix, after, b.ms())
			if err != nil {
				yield("", err)
				return
			}
			for _, k := range page {
				if !yield(k, nil) {
					return
				}
			}
			if len(page) < keysPage {
				return
			}
			after = page[len(page)-1]
		}
	}
}

// DeletePrefix removes every key that starts with prefix.
func (b *backend) DeletePrefix(ctx context.Context, prefix string) error {
	_, err := b.db.Exec(ctx, b.q.deletePrefix, b.q.prefixLen(prefix), prefix)
	return err
}

// Ping pings the database.
func (b *backend) Ping(ctx context.Context) error {
	return b.db.Ping(ctx)
}

// Close does nothing: the application owns db.
func (b *backend) Close() error {
	return nil
}
