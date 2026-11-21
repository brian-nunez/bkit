package rdb

import (
	"errors"
	"io/fs"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
)

func TestSplitMigration(t *testing.T) {
	tests := []struct {
		name    string
		dialect Dialect
		src     string
		want    []statement
		noTx    bool
	}{
		{
			name:    "semicolons end statements",
			dialect: SQLite,
			src:     "CREATE TABLE a (x INT);\nINSERT INTO a VALUES (1);\n\n  INSERT INTO a VALUES (2)",
			want:    []statement{{"CREATE TABLE a (x INT)", 1}, {"INSERT INTO a VALUES (1)", 2}, {"INSERT INTO a VALUES (2)", 4}},
		},
		{
			name:    "empty statements and comments are skipped",
			dialect: Postgres,
			src:     "-- only a comment;\n;;\n/* block; */\nSELECT 1; -- trailing; comment\n",
			want:    []statement{{"/* block; */\nSELECT 1", 4}},
		},
		{
			name:    "unicode spaces are code",
			dialect: Postgres,
			src:     "SELECT 1; ;\n \nSELECT 2;\f\v",
			want:    []statement{{"SELECT 1", 1}, {" ", 1}, {" \nSELECT 2", 2}},
		},
		{
			name:    "quotes hide semicolons",
			dialect: SQLite,
			src:     `SELECT 'a;b', "c;d", 'it''s;';SELECT 2`,
			want:    []statement{{`SELECT 'a;b', "c;d", 'it''s;'`, 1}, {"SELECT 2", 1}},
		},
		{
			name:    "sqlite brackets and backticks",
			dialect: SQLite,
			src:     "SELECT [a;b], `c;d`; SELECT 2",
			want:    []statement{{"SELECT [a;b], `c;d`", 1}, {"SELECT 2", 1}},
		},
		{
			name:    "sqlite block comments do not nest",
			dialect: SQLite,
			src:     "SELECT 1 /* /* */; SELECT 2",
			want:    []statement{{"SELECT 1 /* /* */", 1}, {"SELECT 2", 1}},
		},
		{
			name:    "postgres block comments nest",
			dialect: Postgres,
			src:     "SELECT 1 /* /* ; */ ; */; SELECT 2",
			want:    []statement{{"SELECT 1 /* /* ; */ ; */", 1}, {"SELECT 2", 1}},
		},
		{
			name:    "postgres dollar quotes and escape strings",
			dialect: Postgres,
			src:     "CREATE FUNCTION f() RETURNS int AS $body$ BEGIN RETURN 1; END $body$ LANGUAGE plpgsql;\nSELECT E'a\\';b', 'c\\';SELECT 3",
			want: []statement{
				{"CREATE FUNCTION f() RETURNS int AS $body$ BEGIN RETURN 1; END $body$ LANGUAGE plpgsql", 1},
				{"SELECT E'a\\';b', 'c\\'", 2},
				{"SELECT 3", 2},
			},
		},
		{
			name:    "postgres brackets and hashes are code",
			dialect: Postgres,
			src:     "SELECT a[1] # 2; SELECT `x`",
			want:    []statement{{"SELECT a[1] # 2", 1}, {"SELECT `x`", 1}},
		},
		{
			name:    "mysql backslashes, hashes, and backticks",
			dialect: MySQL,
			src:     "SELECT 'a\\';b', \"c\\\";d\", `e;f`; # comment; here\nSELECT 2",
			want:    []statement{{"SELECT 'a\\';b', \"c\\\";d\", `e;f`", 1}, {"# comment; here\nSELECT 2", 2}},
		},
		{
			name:    "mysql dashes need a space to start a comment",
			dialect: MySQL,
			src:     "SELECT 1--1; SELECT 2 -- c;\nSELECT 3; SELECT 4 --",
			want:    []statement{{"SELECT 1--1", 1}, {"SELECT 2 -- c;\nSELECT 3", 1}, {"SELECT 4 --", 2}},
		},
		{
			name:    "unterminated comments and brackets run to the end",
			dialect: SQLite,
			src:     "SELECT [a; SELECT 2",
			want:    []statement{{"SELECT [a; SELECT 2", 1}},
		},
		{
			name:    "unterminated sqlite block comment",
			dialect: SQLite,
			src:     "SELECT 1 /* ; SELECT 2",
			want:    []statement{{"SELECT 1 /* ; SELECT 2", 1}},
		},
		{
			name:    "crlf line endings",
			dialect: SQLite,
			src:     "-- rdb:no-transaction\r\nSELECT 1;\r\nSELECT 2;\r\n",
			want:    []statement{{"SELECT 1", 2}, {"SELECT 2", 3}},
			noTx:    true,
		},
		{
			name:    "no-transaction directive",
			dialect: Postgres,
			src:     "-- A comment first.\n  --rdb: no-transaction  \nCREATE INDEX CONCURRENTLY i ON t (x)",
			want:    []statement{{"CREATE INDEX CONCURRENTLY i ON t (x)", 3}},
			noTx:    true,
		},
		{
			name:    "block",
			dialect: SQLite,
			src: "CREATE TABLE a (x INT);\n-- rdb:begin-statement\n\n\nCREATE TRIGGER t AFTER INSERT ON a BEGIN\n  SELECT 1;\nEND;\n" +
				"-- rdb:end-statement\nSELECT 2;\n-- rdb:begin-statement\nSELECT 3;\n-- rdb:end-statement",
			want: []statement{
				{"CREATE TABLE a (x INT)", 1},
				{"CREATE TRIGGER t AFTER INSERT ON a BEGIN\n  SELECT 1;\nEND;", 5},
				{"SELECT 2", 9},
				{"SELECT 3;", 11},
			},
		},
		{
			name:    "empty file",
			dialect: MySQL,
			src:     "-- nothing to do\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			noTx, got, err := splitMigration(tt.src, tt.dialect)
			if err != nil {
				t.Fatalf("splitMigration: %v", err)
			}
			if noTx != tt.noTx {
				t.Errorf("noTx = %v, want %v", noTx, tt.noTx)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("statements =\n%+v\nwant\n%+v", got, tt.want)
			}
		})
	}
}

func TestSplitMigrationErrors(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{"no-transaction after a statement", "SELECT 1;\n-- rdb:no-transaction\n", "line 2: no-transaction must come once"},
		{"no-transaction twice", "-- rdb:no-transaction\n-- rdb:no-transaction\n", "line 2: no-transaction must come once"},
		{"directive inside a statement", "SELECT\n-- rdb:no-transaction\n1;", `line 2: directive "no-transaction" inside the statement that starts on line 1`},
		{"unknown directive", "-- rdb:no-transactions\n", `line 1: unknown directive "no-transactions"`},
		{"end without begin", "-- rdb:end-statement", "line 1: end-statement without begin-statement"},
		{"begin without end", "-- rdb:begin-statement\nSELECT 1;\n", "line 1: begin-statement without end-statement"},
		{"empty block", "-- rdb:begin-statement\n \n-- rdb:end-statement\n", "line 1: empty begin-statement block"},
		{"directive inside a block", "-- rdb:begin-statement\n-- rdb:no-transaction\n-- rdb:end-statement\n", `line 2: directive "no-transaction" inside the block that starts on line 1`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := splitMigration(tt.src, SQLite)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

// file returns a MapFile holding src.
func file(src string) *fstest.MapFile {
	return &fstest.MapFile{Data: []byte(src)}
}

func TestLoadMigrations(t *testing.T) {
	fsys := fstest.MapFS{
		"0002_seed.sql":          file("INSERT INTO a VALUES (1);\nINSERT INTO a VALUES (2)"),
		"0001_create.sql":        file("CREATE TABLE a (x INT)"),
		"0001_create.mysql.sql":  file("CREATE TABLE a (x INT) ENGINE=InnoDB"),
		"0001_create.sqlite.sql": file("CREATE TABLE a (x INTEGER)"),
		"0003_index.sql":         file("-- rdb:no-transaction\nCREATE INDEX i ON a (x)"),
		"README.md":              file("not a migration"),
		"sub/0004_nested.sql":    file("SELECT 1"),
	}
	set, err := loadMigrations(fsys, MySQL)
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	if len(set) != 3 {
		t.Fatalf("loaded %d migrations, want 3", len(set))
	}
	want := []struct {
		version int64
		name    string
		file    string
		noTx    bool
		stmts   int
	}{
		{1, "create", "0001_create.mysql.sql", false, 1},
		{2, "seed", "0002_seed.sql", false, 2},
		{3, "index", "0003_index.sql", true, 1},
	}
	for i, w := range want {
		m := set[i]
		if m.version != w.version || m.name != w.name || m.file != w.file || m.noTx != w.noTx || len(m.statements) != w.stmts {
			t.Errorf("migration %d = %+v, want %+v", i, m, w)
		}
	}
	if set[0].checksum != checksum("CREATE TABLE a (x INT) ENGINE=InnoDB") {
		t.Error("checksum is not the chosen file's")
	}
	pg, err := loadMigrations(fsys, Postgres)
	if err != nil || pg[0].file != "0001_create.sql" {
		t.Fatalf("Postgres loaded %v, %v; want the plain file", pg, err)
	}
}

func TestChecksumIgnoresCRLF(t *testing.T) {
	if checksum("a\r\nb\n") != checksum("a\nb\n") {
		t.Fatal("CRLF changed the checksum")
	}
	if checksum("a\nb") == checksum("a\nc") {
		t.Fatal("different contents share a checksum")
	}
}

// errFS fails ReadDir, or ReadFile for the file named bad.
type errFS struct {
	files fstest.MapFS
	bad   string
}

var errRead = errors.New("read failed")

func (f errFS) Open(name string) (fs.File, error) {
	if name == "." && f.bad == "" || name == f.bad {
		return nil, errRead
	}
	return f.files.Open(name)
}

func TestLoadMigrationsErrors(t *testing.T) {
	tests := []struct {
		name string
		fsys fs.FS
		want string
	}{
		{"read dir", errFS{files: fstest.MapFS{}}, "read failed"},
		{"read file", errFS{files: fstest.MapFS{"1_a.sql": file("SELECT 1")}, bad: "1_a.sql"}, "read failed"},
		{"empty", fstest.MapFS{"README.md": file("")}, "no .sql files in the root"},
		{"bad name", fstest.MapFS{"create.sql": file("")}, "create.sql: the name must be"},
		{"no name", fstest.MapFS{"1.sql": file("")}, "1.sql: the name must be"},
		{"zero version", fstest.MapFS{"0_a.sql": file("")}, "0_a.sql: the version must be a positive 64-bit integer"},
		{"huge version", fstest.MapFS{"99999999999999999999_a.sql": file("")}, "the version must be a positive 64-bit integer"},
		{"long name", fstest.MapFS{"1_" + strings.Repeat("a", 256) + ".sql": file("")}, "the name is longer than 255 bytes"},
		{"unknown dialect", fstest.MapFS{"1_a.postgress.sql": file("")}, `1_a.postgress.sql: unknown dialect "postgress"`},
		{"two names", fstest.MapFS{"1_a.sql": file(""), "1_b.sql": file("")}, `1_b.sql: version 1 also belongs to migration "a"`},
		{"duplicate", fstest.MapFS{"1_a.sql": file(""), "01_a.sql": file("")}, "version 1 already has the file"},
		{"no file for dialect", fstest.MapFS{"1_a.postgres.sql": file("")}, `migration 1 "a" has no file for dialect sqlite`},
		{"bad directive", fstest.MapFS{"1_a.sql": file("-- rdb:nope")}, `1_a.sql: line 1: unknown directive "nope"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := loadMigrations(tt.fsys, SQLite)
			if !errors.Is(err, ErrInvalidMigration) {
				t.Fatalf("error = %v, want ErrInvalidMigration", err)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %q, want it to contain %q", err, tt.want)
			}
		})
	}
}
