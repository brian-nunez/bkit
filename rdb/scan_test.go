package rdb

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

type audit struct {
	CreatedBy string
}

type Timestamps struct {
	CreatedAt time.Time
}

type user struct {
	ID       int64  `db:"user_id"`
	Email    string `db:"EMAIL"`
	FullName string
	Password string `db:"-"`
	Nick     sql.NullString
	secret   string //nolint:unused // proves unexported fields are skipped
	audit
	Timestamps
}

var userCols = []string{"user_id", "email", "full_name", "created_by", "createdat", "nick"}

func userRow(id int64, email string) []driver.Value {
	return []driver.Value{id, email, "Ada " + email, "admin", time.Unix(0, 0).UTC(), nil}
}

func scanDB(t *testing.T, cols []string, data ...[]driver.Value) (*DB, *testDriver) {
	t.Helper()
	d := newDriver(SQLite)
	d.db.query = rowsOf(cols, data...)
	return mustOpen(t, d), d
}

func TestOneStruct(t *testing.T) {
	db, _ := scanDB(t, userCols, userRow(7, "a@x"), userRow(8, "b@x"))
	for range 2 { // the second call uses the cached field set
		u, err := One[user](context.Background(), db, "SELECT")
		if err != nil {
			t.Fatalf("One: %v", err)
		}
		if u.ID != 7 || u.Email != "a@x" || u.FullName != "Ada a@x" || u.CreatedBy != "admin" ||
			!u.CreatedAt.Equal(time.Unix(0, 0)) || u.Nick.Valid {
			t.Errorf("One = %+v", u)
		}
	}
}

func TestOneScalar(t *testing.T) {
	ctx := context.Background()
	db, _ := scanDB(t, []string{"count"}, []driver.Value{int64(42)})
	n, err := One[int](ctx, db, "SELECT count(*)")
	if err != nil || n != 42 {
		t.Fatalf("One[int] = %d, %v", n, err)
	}

	when := time.Unix(10, 0).UTC()
	db, _ = scanDB(t, []string{"at"}, []driver.Value{when})
	at, err := One[time.Time](ctx, db, "SELECT")
	if err != nil || !at.Equal(when) {
		t.Fatalf("One[time.Time] = %v, %v", at, err)
	}

	db, _ = scanDB(t, []string{"nick"}, []driver.Value{"ada"})
	nick, err := One[sql.NullString](ctx, db, "SELECT")
	if err != nil || nick.String != "ada" {
		t.Fatalf("One[sql.NullString] = %v, %v", nick, err)
	}

	db, _ = scanDB(t, []string{"a", "b"}, []driver.Value{1, 2})
	if _, err := One[int](ctx, db, "SELECT"); !errors.Is(err, ErrScan) {
		t.Fatalf("two columns into int: err = %v, want ErrScan", err)
	}
}

func TestOneErrors(t *testing.T) {
	ctx := context.Background()

	db, _ := scanDB(t, []string{"n"})
	_, err := One[int](ctx, db, "SELECT")
	if !errors.Is(err, ErrNotFound) || !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("no rows: err = %v", err)
	}

	d := newDriver(SQLite)
	d.db.query = func(string, []driver.NamedValue) (driver.Rows, error) {
		return &fakeRows{cols: []string{"n"}, err: errUnique}, nil
	}
	db = mustOpen(t, d)
	if _, err := One[int](ctx, db, "SELECT"); !errors.Is(err, ErrUniqueViolation) {
		t.Fatalf("rows error: err = %v", err)
	}

	d.db.query = func(string, []driver.NamedValue) (driver.Rows, error) { return nil, errUnique }
	if _, err := One[int](ctx, db, "SELECT"); !errors.Is(err, ErrUniqueViolation) {
		t.Fatalf("query error: err = %v", err)
	}

	d.db.query = rowsOf([]string{"n"}, []driver.Value{"not a number"})
	if _, err := One[int](ctx, db, "SELECT"); !errors.Is(err, ErrScan) {
		t.Fatalf("conversion error: err = %v", err)
	}

	d.db.query = rowsOf([]string{"n", "extra"}, []driver.Value{1, 2})
	if _, err := One[struct{ N int }](ctx, db, "SELECT"); !errors.Is(err, ErrScan) {
		t.Fatalf("unmatched column: err = %v", err)
	}
}

func TestStrictScan(t *testing.T) {
	ctx := context.Background()
	cols := []string{"user_id", "unknown"}
	db, _ := scanDB(t, cols, []driver.Value{int64(1), "x"})
	_, err := One[user](ctx, db, "SELECT")
	if !errors.Is(err, ErrScan) || !strings.Contains(err.Error(), `column "unknown" has no field`) {
		t.Fatalf("strict: err = %v", err)
	}

	d := newDriver(SQLite)
	d.db.query = rowsOf(cols, []driver.Value{int64(1), "x"})
	lenient := mustOpen(t, d, WithoutStrictScan())
	u, err := One[user](ctx, lenient, "SELECT")
	if err != nil || u.ID != 1 {
		t.Fatalf("lenient: %+v, %v", u, err)
	}
	if _, err := One[user](ctx, lenient.Raw(), "SELECT"); err != nil {
		t.Fatalf("lenient Raw: %v", err)
	}
	if err := lenient.Tx(ctx, func(tx *Tx) error {
		_, err := One[user](ctx, tx, "SELECT")
		return err
	}); err != nil {
		t.Fatalf("lenient Tx: %v", err)
	}
}

func TestFieldMatching(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name string
		cols []string
		want string // substring of the ErrScan message, or "" for success
	}{
		{"skipped field", []string{"password"}, `column "password" has no field`},
		{"unexported field", []string{"secret"}, `column "secret" has no field`},
		{"tag beats name", []string{"id"}, `column "id" has no field`},
		{"two columns one field", []string{"email", "EMAIL"}, `columns "email" and "EMAIL" both map to field Email`},
		{"embedded field by path", []string{"CREATED_BY"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			row := make([]driver.Value, len(tt.cols))
			for i := range row {
				row[i] = "v"
			}
			db, _ := scanDB(t, tt.cols, row)
			_, err := One[user](ctx, db, "SELECT")
			if tt.want == "" {
				if err != nil {
					t.Fatalf("err = %v", err)
				}
				return
			}
			if !errors.Is(err, ErrScan) || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestAmbiguousFields(t *testing.T) {
	ctx := context.Background()
	type byTag struct {
		A string `db:"x"`
		B string `db:"X"`
	}
	db, _ := scanDB(t, []string{"x"}, []driver.Value{"v"})
	if _, err := One[byTag](ctx, db, "SELECT"); !errors.Is(err, ErrScan) || !strings.Contains(err.Error(), "matches fields A and B") {
		t.Fatalf("tags: err = %v", err)
	}

	type byName struct {
		CreatedAt  string
		Created_At string //nolint:staticcheck // deliberately ambiguous with CreatedAt
	}
	db, _ = scanDB(t, []string{"created_at"}, []driver.Value{"v"})
	if _, err := One[byName](ctx, db, "SELECT"); !errors.Is(err, ErrScan) || !strings.Contains(err.Error(), "matches fields CreatedAt and Created_At") {
		t.Fatalf("names: err = %v", err)
	}
}

func TestAll(t *testing.T) {
	ctx := context.Background()
	db, _ := scanDB(t, userCols, userRow(1, "a"), userRow(2, "b"))
	users, err := All[user](ctx, db, "SELECT")
	if err != nil {
		t.Fatal(err)
	}
	ids := []int64{users[0].ID, users[1].ID}
	if !slices.Equal(ids, []int64{1, 2}) {
		t.Errorf("ids = %v", ids)
	}

	db, _ = scanDB(t, []string{"n"})
	empty, err := All[int](ctx, db, "SELECT")
	if err != nil || empty == nil || len(empty) != 0 {
		t.Errorf("All with no rows = %#v, %v; want empty non-nil", empty, err)
	}

	db, _ = scanDB(t, []string{"n"}, []driver.Value{"x"})
	if got, err := All[int](ctx, db, "SELECT"); !errors.Is(err, ErrScan) || got != nil {
		t.Errorf("All = %v, %v; want nil, ErrScan", got, err)
	}
}

func TestEach(t *testing.T) {
	ctx := context.Background()
	db, _ := scanDB(t, []string{"n"}, []driver.Value{int64(1)}, []driver.Value{int64(2)}, []driver.Value{int64(3)})
	var got []int
	for n, err := range Each[int](ctx, db, "SELECT") {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, n)
		if n == 2 {
			break
		}
	}
	if !slices.Equal(got, []int{1, 2}) {
		t.Errorf("Each = %v", got)
	}

	collectErr := func(seq func(func(int, error) bool)) error {
		for _, err := range seq {
			if err != nil {
				return err
			}
		}
		return nil
	}

	d := newDriver(SQLite)
	db = mustOpen(t, d)
	d.db.query = func(string, []driver.NamedValue) (driver.Rows, error) { return nil, errUnique }
	if err := collectErr(Each[int](ctx, db, "SELECT")); !errors.Is(err, ErrUniqueViolation) {
		t.Errorf("query error: %v", err)
	}
	d.db.query = rowsOf([]string{"a", "b"})
	if err := collectErr(Each[int](ctx, db, "SELECT")); !errors.Is(err, ErrScan) {
		t.Errorf("column error: %v", err)
	}
	d.db.query = func(string, []driver.NamedValue) (driver.Rows, error) {
		return &fakeRows{cols: []string{"n"}, data: [][]driver.Value{{int64(1)}}, err: errUnique}, nil
	}
	if err := collectErr(Each[int](ctx, db, "SELECT")); !errors.Is(err, ErrUniqueViolation) {
		t.Errorf("rows error: %v", err)
	}
}

// foreignQuerier is a Querier implemented outside the package.
type foreignQuerier struct {
	db         *DB
	closeFirst bool
}

func (f foreignQuerier) Exec(ctx context.Context, q string, args ...any) (sql.Result, error) {
	return f.db.Exec(ctx, q, args...)
}

func (f foreignQuerier) Query(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	rows, err := f.db.Query(ctx, q, args...)
	if err == nil && f.closeFirst {
		_ = rows.Close()
	}
	return rows, err
}

func (f foreignQuerier) Dialect() Dialect { return f.db.Dialect() }
func (f foreignQuerier) Raw() Querier     { return f }

func TestForeignQuerier(t *testing.T) {
	ctx := context.Background()
	d := newDriver(SQLite)
	db := mustOpen(t, d, WithoutStrictScan())
	q := foreignQuerier{db: db}

	d.db.query = rowsOf([]string{"n", "extra"}, []driver.Value{int64(1), "x"})
	if _, err := One[struct{ N int }](ctx, q, "SELECT"); !errors.Is(err, ErrScan) {
		t.Errorf("foreign Querier scans strictly: err = %v", err)
	}

	d.db.query = func(string, []driver.NamedValue) (driver.Rows, error) {
		return &fakeRows{cols: []string{"n"}, err: errUnique}, nil
	}
	_, err := One[int](ctx, q, "SELECT")
	if !errors.Is(err, errUnique) || errors.Is(err, ErrUniqueViolation) {
		t.Errorf("foreign Querier rows error passes through unclassified: err = %v", err)
	}

	d.db.query = rowsOf([]string{"n"}, []driver.Value{int64(1)})
	q.closeFirst = true
	if _, err := One[int](ctx, q, "SELECT"); !errors.Is(err, ErrScan) {
		t.Errorf("closed rows: err = %v, want ErrScan", err)
	}
	if q.Raw() == nil || q.Dialect() != SQLite {
		t.Error("foreign Querier helpers")
	}
	if _, err := q.Exec(ctx, "X"); err != nil {
		t.Error(err)
	}
}
