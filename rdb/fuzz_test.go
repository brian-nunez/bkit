package rdb

import (
	"errors"
	"io/fs"
	"maps"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
)

// rebindSeeds are inputs that stress the Postgres lexer in rebindDollar.
var rebindSeeds = []string{
	"",
	"?",
	"??",
	"SELECT ?, ?",
	"SELECT 1",
	"SELECT data ?| array['a'] FROM t WHERE data ?& array['b'] AND x = ?",
	"SELECT '{\"a\":1}'::jsonb ? 'a', '? in a string', ?",
	"SELECT 'unterminated ?",
	`SELECT "unterminated ?`,
	`SELECT "quoted ?", "a""b?", ?`,
	`SELECT 'it''s ?', ?`,
	`SELECT E'\'?', ?`,
	`SELECT e'\\', ?`,
	`SELECT E'\`,
	`SELECT xe'?', ?`,
	"SELECT $$ ? $$, ?",
	"SELECT $tag$ ? $tag$, ?",
	"SELECT $tag$ ? $other$ ?",
	"SELECT $1, ?, a$b$c, ?",
	"SELECT $1$ ?",
	"SELECT ?$a$ ? $a$",
	"SELECT 1 -- ? comment\n, ?",
	"SELECT ? --",
	"SELECT ? -- ?",
	"SELECT 1 /* ? /* ? */ ? */ ?",
	"SELECT 1 /* unterminated ?",
	"SELECT 1 /*/ ? */ ?",
	"-",
	"/",
	"$",
	"'",
	"\"",
	"\xff?\x80$x$?$x$",
}

// FuzzRebindDollar checks rebindDollar on arbitrary input: it never panics,
// it only replaces ? with $1..$n in order, and it returns input without ?
// unchanged.
func FuzzRebindDollar(f *testing.F) {
	for _, s := range rebindSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, query string) {
		out := rebindDollar(query)
		n := checkRebind(t, query, out)
		if !strings.Contains(query, "?") && (out != query || n != 0) {
			t.Fatalf("rebindDollar(%q) = %q, want the input unchanged", query, out)
		}
		if got := strings.Count(query, "?") - strings.Count(out, "?"); got != n {
			t.Fatalf("rebindDollar(%q) removed %d ? but inserted %d placeholders", query, got, n)
		}
	})
}

// checkRebind walks in and out together and fails unless out is in with
// some ? replaced by $1, $2, ... $n in order. It returns n.
func checkRebind(t *testing.T, in, out string) int {
	t.Helper()
	n, j := 0, 0
	for i := range len(in) {
		if j < len(out) && in[i] == out[j] {
			j++
			continue
		}
		want := "$" + strconv.Itoa(n+1)
		if in[i] != '?' || !strings.HasPrefix(out[j:], want) {
			t.Fatalf("rebindDollar(%q) = %q: byte %d of the output is neither the input byte %q nor %s", in, out, j, in[i], want)
		}
		n++
		j += len(want)
	}
	if j != len(out) {
		t.Fatalf("rebindDollar(%q) = %q: trailing output %q", in, out, out[j:])
	}
	return n
}

// rebindFragment is a self-contained piece of Postgres SQL and its text
// after rebindDollar. A placeholder fragment has want "" and receives the
// next $N.
type rebindFragment struct {
	sql  string
	want string
}

var rebindFragments = []rebindFragment{
	{"?", ""},
	{"SELECT", "SELECT"},
	{"col_1", "col_1"},
	{"a$b", "a$b"},
	{"$7", "$7"},
	{"=", "="},
	{"::jsonb", "::jsonb"},
	{"?|", ""},
	{"?&", ""},
	{"'it''s ?'", "'it''s ?'"},
	{"'{\"k\":1}'::jsonb ? 'k'", ""},
	{`E'\'?'`, `E'\'?'`},
	{`e'\\?'`, `e'\\?'`},
	{`"col ?"`, `"col ?"`},
	{`"a""?"`, `"a""?"`},
	{"$$ ? $$", "$$ ? $$"},
	{"$fn$ ? ; '?' $fn$", "$fn$ ? ; '?' $fn$"},
	{"-- ? comment\n", "-- ? comment\n"},
	{"/* ? /* nested ? */ ? */", "/* ? /* nested ? */ ? */"},
}

// TestRebindDollarProperties builds random queries from fragments whose
// rewrite is known and checks rebindDollar against the expected text, the
// structural invariants, and idempotence, which holds for these queries
// because every fragment ends cleanly and fragments are space separated.
func TestRebindDollarProperties(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for range 5000 {
		var in, want []string
		n := 0
		for range r.IntN(12) {
			frag := rebindFragments[r.IntN(len(rebindFragments))]
			in = append(in, frag.sql)
			switch frag.sql {
			case "?", "?|", "?&":
				n++
				want = append(want, "$"+strconv.Itoa(n)+frag.sql[1:])
			case "'{\"k\":1}'::jsonb ? 'k'":
				n++
				want = append(want, "'{\"k\":1}'::jsonb $"+strconv.Itoa(n)+" 'k'")
			default:
				want = append(want, frag.want)
			}
		}
		query := strings.Join(in, " ")
		out := rebindDollar(query)
		if w := strings.Join(want, " "); out != w {
			t.Fatalf("rebindDollar(%q) = %q, want %q", query, out, w)
		}
		if got := checkRebind(t, query, out); got != n {
			t.Fatalf("rebindDollar(%q) inserted %d placeholders, want %d", query, got, n)
		}
		if again := rebindDollar(out); again != out {
			t.Fatalf("rebindDollar is not idempotent on %q: %q then %q", query, out, again)
		}
	}
}

// migrationSeeds are migration sources that stress splitMigration.
var migrationSeeds = []string{
	"",
	"\n",
	";",
	"SELECT 1",
	"CREATE TABLE a (x INT);\nINSERT INTO a VALUES (1);\n",
	"-- rdb:no-transaction\nCREATE INDEX CONCURRENTLY i ON t (x);",
	"-- rdb:no-transaction\n-- rdb:no-transaction\nSELECT 1;",
	"SELECT 1;\n-- rdb:no-transaction\n",
	"SELECT 1\n-- rdb:no-transaction\n;",
	"-- rdb:begin-statement\nCREATE FUNCTION f() AS $$ BEGIN; END $$;\n-- rdb:end-statement\nSELECT 2;",
	"-- rdb:begin-statement\n\n-- rdb:end-statement\n",
	"-- rdb:begin-statement\nSELECT 1;",
	"-- rdb:begin-statement\n-- rdb:no-transaction\n-- rdb:end-statement",
	"-- rdb:end-statement\n",
	"-- rdb:unknown\n",
	"  --   rdb:no-transaction  \r\nSELECT 1;\r\n",
	"SELECT 'a;b', \"c;d\", `e;f`, [g;h]; SELECT 2",
	"SELECT 'unterminated;",
	"SELECT 'a\\';b'; SELECT 2",
	"SELECT E'a\\';b'; SELECT 2",
	"SELECT $tag$ ; $tag$; SELECT 2",
	"SELECT $tag$ ; unterminated",
	"SELECT 1 /* /* ; */ ; */; SELECT 2",
	"SELECT 1 /* unterminated ;",
	"SELECT 1 # ; mysql comment\n; SELECT 2",
	"SELECT 1 --; mysql needs a space\n; SELECT 2",
	"SELECT 1 --",
	"SELECT 'x\n-- rdb:no-transaction\n';",
	"--rdb: no-transaction\n\tSELECT 1",
}

// FuzzSplitMigration checks splitMigration on arbitrary sources for every
// dialect.
func FuzzSplitMigration(f *testing.F) {
	for _, s := range migrationSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, src string) {
		for _, d := range migrationDialects {
			checkSplit(t, src, d)
		}
	})
}

// checkSplit checks the invariants of splitMigration(src, d): statements
// are non-empty, appear in src in order, and start on a plausible line;
// no-transaction is reported only when the directive is present; prefixing
// the directive turns noTx on without changing the statements, or fails
// when the source already has it; and loadMigrations reports any failure
// as ErrInvalidMigration.
func checkSplit(t *testing.T, src string, d Dialect) {
	t.Helper()
	noTx, stmts, err := splitMigration(src, d)
	if err != nil {
		_, lerr := loadMigrations(fstest.MapFS{"1_m.sql": {Data: []byte(src)}}, d)
		if !errors.Is(lerr, ErrInvalidMigration) {
			t.Fatalf("%s: splitMigration(%q) failed with %v, but loadMigrations returned %v", d, src, err, lerr)
		}
		return
	}
	if noTx && !hasNoTxLine(src) {
		t.Fatalf("%s: splitMigration(%q) reported no-transaction without the directive", d, src)
	}
	lines := strings.Count(src, "\n") + 1
	pos, prevLine := 0, 1
	for _, st := range stmts {
		if strings.TrimSpace(st.sql) == "" {
			t.Fatalf("%s: splitMigration(%q) returned an empty statement %q", d, src, st.sql)
		}
		k := strings.Index(src[pos:], st.sql)
		if k < 0 {
			t.Fatalf("%s: splitMigration(%q): statement %q is not in the source after byte %d", d, src, st.sql, pos)
		}
		at := 1 + strings.Count(src[:pos+k], "\n")
		if st.line < at || st.line < prevLine || st.line > lines {
			t.Fatalf("%s: splitMigration(%q): statement %q on line %d, want between %d and %d", d, src, st.sql, st.line, max(at, prevLine), lines)
		}
		pos += k + len(st.sql)
		prevLine = st.line
	}

	prefixed := "-- rdb:" + directiveNoTx + "\n" + src
	pNoTx, pStmts, pErr := splitMigration(prefixed, d)
	if noTx {
		if pErr == nil {
			t.Fatalf("%s: splitMigration(%q) accepted a second no-transaction directive", d, prefixed)
		}
		return
	}
	if pErr != nil || !pNoTx || len(pStmts) != len(stmts) {
		t.Fatalf("%s: splitMigration(%q) = %v, %d statements, %v; want true, %d statements, nil", d, prefixed, pNoTx, len(pStmts), pErr, len(stmts))
	}
	for i, st := range pStmts {
		if st.sql != stmts[i].sql || st.line != stmts[i].line+1 {
			t.Fatalf("%s: with the directive, statement %d is %+v, want %q on line %d", d, i, st, stmts[i].sql, stmts[i].line+1)
		}
	}
}

// hasNoTxLine reports whether some line of src, apart from spaces, is a
// line comment that reads rdb:no-transaction.
func hasNoTxLine(src string) bool {
	for line := range strings.Lines(src) {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "--")
		if !ok {
			continue
		}
		name, ok := strings.CutPrefix(strings.TrimSpace(rest), "rdb:")
		if ok && strings.TrimSpace(name) == directiveNoTx {
			return true
		}
	}
	return false
}

// FuzzLoadMigrations checks loadMigrations on file systems built from
// fuzzed file names and contents.
func FuzzLoadMigrations(f *testing.F) {
	f.Add("1_init.sql", "CREATE TABLE a (x INT);", "2_more.sql", "SELECT 1;", "", "", uint8(0))
	f.Add("1_init.sql", "SELECT 1;", "1_init.postgres.sql", "SELECT $$;$$;", "1_init.mysql.sql", "SELECT 1 # c\n;", uint8(1))
	f.Add("1_init.sqlite.sql", "SELECT 1;", "2_x.sql", "SELECT 2;", "", "", uint8(2))
	f.Add("1_a.sql", "SELECT 1;", "1_b.sql", "SELECT 2;", "", "", uint8(0))
	f.Add("1_a.sql", "SELECT 1;", "1_a.sql", "SELECT 2;", "1_a.oracle.sql", "", uint8(0))
	f.Add("0_zero.sql", "SELECT 1;", "", "", "", "", uint8(0))
	f.Add("99999999999999999999_big.sql", "SELECT 1;", "", "", "", "", uint8(0))
	f.Add("bad name.sql", "", "readme.txt", "x", "dir/1_nested.sql", "SELECT 1;", uint8(0))
	f.Add("10_b.sql", "SELECT 10;", "9_a.sql", "SELECT 9;", "000011_c.sql", "-- rdb:no-transaction\nSELECT 11;", uint8(1))
	f.Add("1_x.sql", "-- rdb:end-statement\n", "", "", "", "", uint8(0))
	f.Add("readme.md", "", "", "", "", "", uint8(0))
	f.Add("1_"+strings.Repeat("n", maxMigrationName+1)+".sql", "", "", "", "", "", uint8(0))
	f.Fuzz(func(t *testing.T, n1, c1, n2, c2, n3, c3 string, di uint8) {
		d := migrationDialects[int(di)%len(migrationDialects)]
		fsys := fstest.MapFS{}
		for _, file := range [][2]string{{n1, c1}, {n2, c2}, {n3, c3}} {
			if fs.ValidPath(file[0]) && file[0] != "." {
				fsys[file[0]] = &fstest.MapFile{Data: []byte(file[1])}
			}
		}
		set, err := loadMigrations(fsys, d)
		if err != nil {
			var e *Error
			if !errors.Is(err, ErrInvalidMigration) || !errors.As(err, &e) || e.Op != "migrate" {
				t.Fatalf("loadMigrations(%v, %s) = %v, want a migrate *Error matching ErrInvalidMigration", slices.Sorted(maps.Keys(fsys)), d, err)
			}
			return
		}
		if len(set) == 0 {
			t.Fatalf("loadMigrations(%v, %s) returned no migrations and no error", slices.Sorted(maps.Keys(fsys)), d)
		}
		for i, m := range set {
			if m.version < 1 || (i > 0 && m.version <= set[i-1].version) {
				t.Fatalf("loadMigrations(%v, %s): versions not sorted and unique at %d: %d", slices.Sorted(maps.Keys(fsys)), d, i, m.version)
			}
			file, ok := fsys[m.file]
			if !ok || strings.Contains(m.file, "/") {
				t.Fatalf("loadMigrations(%v, %s) returned %q, not a root file", slices.Sorted(maps.Keys(fsys)), d, m.file)
			}
			if !strings.HasPrefix(strings.TrimLeft(m.file, "0"), strconv.FormatInt(m.version, 10)+"_") {
				t.Fatalf("file %q does not carry version %d", m.file, m.version)
			}
			if fd := Dialect(migrationFile.FindStringSubmatch(m.file)[3]); fd != "" && fd != d {
				t.Fatalf("loadMigrations chose the %s file %q for dialect %s", fd, m.file, d)
			}
			if _, ok := fsys[strings.TrimSuffix(m.file, ".sql")+"."+string(d)+".sql"]; ok {
				t.Fatalf("loadMigrations chose %q over the %s file", m.file, d)
			}
			if m.checksum != checksum(string(file.Data)) {
				t.Fatalf("file %q: checksum %s does not match its contents", m.file, m.checksum)
			}
			noTx, stmts, serr := splitMigration(string(file.Data), d)
			if serr != nil || noTx != m.noTx || !slices.Equal(stmts, m.statements) {
				t.Fatalf("file %q: loadMigrations disagrees with splitMigration", m.file)
			}
		}
	})
}
