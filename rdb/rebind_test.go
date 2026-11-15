package rdb

import "testing"

func TestRebindDollar(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"no placeholders", "SELECT 1", "SELECT 1"},
		{"placeholders", "SELECT * FROM t WHERE a = ? AND b IN (?, ?)", "SELECT * FROM t WHERE a = $1 AND b IN ($2, $3)"},
		{"string constant", "SELECT '?', ?", "SELECT '?', $1"},
		{"doubled quote", "SELECT 'it''s ?', ?", "SELECT 'it''s ?', $1"},
		{"escape string", `SELECT E'\' ?', ?`, `SELECT E'\' ?', $1`},
		{"lowercase escape string", `SELECT e'\' ?', ?`, `SELECT e'\' ?', $1`},
		{"escape string at start", `E'\' ?' ?`, `E'\' ?' $1`},
		{"identifier ending in e", `SELECT name'\' , ?`, `SELECT name'\' , $1`},
		{"backslash outside escape string", `SELECT '\' , ?`, `SELECT '\' , $1`},
		{"quoted identifier", `SELECT "a?""b", ?`, `SELECT "a?""b", $1`},
		{"line comment", "SELECT ? -- what?\n, ?", "SELECT $1 -- what?\n, $2"},
		{"line comment at end", "SELECT ? -- what?", "SELECT $1 -- what?"},
		{"minus", "SELECT 3-?", "SELECT 3-$1"},
		{"block comment", "SELECT /* ? */ ?", "SELECT /* ? */ $1"},
		{"nested block comment", "SELECT /* a /* ? */ ? */ ?", "SELECT /* a /* ? */ ? */ $1"},
		{"slash", "SELECT 4/?", "SELECT 4/$1"},
		{"dollar quote", "SELECT $$ ? $$, ?", "SELECT $$ ? $$, $1"},
		{"tagged dollar quote", "SELECT $fn$ ? $x$ ? $fn$, ?", "SELECT $fn$ ? $x$ ? $fn$, $1"},
		{"tag with digits", "SELECT $a1$ ? $a1$, ?", "SELECT $a1$ ? $a1$, $1"},
		{"dollar in identifier", "SELECT a$b$c FROM t WHERE x = ?", "SELECT a$b$c FROM t WHERE x = $1"},
		{"doubled dollar in identifier", "SELECT a$$b FROM t WHERE x = ? AND y = $$?$$", "SELECT a$$b FROM t WHERE x = $1 AND y = $$?$$"},
		{"positional parameter", "SELECT $1, ?", "SELECT $1, $1"},
		{"trailing dollar", "SELECT ?, $", "SELECT $1, $"},
		{"unterminated tag", "SELECT ?, $abc", "SELECT $1, $abc"},
		{"unterminated string", "SELECT ?, 'abc ?", "SELECT $1, 'abc ?"},
		{"unterminated block comment", "SELECT ? /* ?", "SELECT $1 /* ?"},
		{"unterminated dollar quote", "SELECT ?, $$ ?", "SELECT $1, $$ ?"},
		{"non-ASCII identifier", "SELECT ü'x', ?", "SELECT ü'x', $1"},
		{"jsonb operator", "SELECT data ? 'k'", "SELECT data $1 'k'"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := rebindDollar(tt.in); got != tt.want {
				t.Errorf("rebindDollar(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
