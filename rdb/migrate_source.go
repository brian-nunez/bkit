package rdb

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// migration is the file one migration runs on the current dialect.
type migration struct {
	version    int64
	name       string
	file       string
	checksum   string
	noTx       bool
	statements []statement
}

// statement is one SQL statement of a migration and the line it starts on.
type statement struct {
	sql  string
	line int
}

// migrationFile matches <version>_<name>.sql and <version>_<name>.<dialect>.sql.
var migrationFile = regexp.MustCompile(`^([0-9]+)_([A-Za-z0-9_-]+)(?:\.([a-z]+))?\.sql$`)

// maxMigrationName is the longest name the history table stores.
const maxMigrationName = 255

// invalidMigration returns an *Error of kind ErrInvalidMigration.
func invalidMigration(format string, args ...any) error {
	return &Error{Op: "migrate", Kind: ErrInvalidMigration, Err: fmt.Errorf(format, args...)}
}

// loadMigrations reads the migrations in the root of fsys and returns, in
// version order, the file each one runs on dialect d.
func loadMigrations(fsys fs.FS, d Dialect) ([]migration, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, &Error{Op: "migrate", Kind: ErrInvalidMigration, Err: err}
	}
	names := map[int64]string{}
	files := map[int64]map[Dialect]string{}
	for _, e := range entries {
		file := e.Name()
		if e.IsDir() || !strings.HasSuffix(file, ".sql") {
			continue
		}
		m := migrationFile.FindStringSubmatch(file)
		if m == nil {
			return nil, invalidMigration("%s: the name must be <version>_<name>.sql or <version>_<name>.<dialect>.sql", file)
		}
		version, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil || version < 1 {
			return nil, invalidMigration("%s: the version must be a positive 64-bit integer", file)
		}
		name, dialect := m[2], Dialect(m[3])
		if len(name) > maxMigrationName {
			return nil, invalidMigration("%s: the name is longer than %d bytes", file, maxMigrationName)
		}
		if dialect != "" && !slices.Contains(migrationDialects, dialect) {
			return nil, invalidMigration("%s: unknown dialect %q", file, dialect)
		}
		if prev, ok := names[version]; ok && prev != name {
			return nil, invalidMigration("%s: version %d also belongs to migration %q", file, version, prev)
		}
		names[version] = name
		if files[version] == nil {
			files[version] = map[Dialect]string{}
		}
		if prev, ok := files[version][dialect]; ok {
			return nil, invalidMigration("%s: version %d already has the file %s", file, version, prev)
		}
		files[version][dialect] = file
	}
	if len(names) == 0 {
		return nil, invalidMigration("no .sql files in the root of the file system; use fs.Sub for a subdirectory")
	}
	set := make([]migration, 0, len(names))
	for _, version := range slices.Sorted(maps.Keys(names)) {
		file, ok := files[version][d]
		if !ok {
			if file, ok = files[version][""]; !ok {
				return nil, invalidMigration("migration %d %q has no file for dialect %s", version, names[version], d)
			}
		}
		data, err := fs.ReadFile(fsys, file)
		if err != nil {
			return nil, &Error{Op: "migrate", Kind: ErrInvalidMigration, Err: err}
		}
		src := string(data)
		noTx, statements, err := splitMigration(src, d)
		if err != nil {
			return nil, invalidMigration("%s: %w", file, err)
		}
		set = append(set, migration{
			version:    version,
			name:       names[version],
			file:       file,
			checksum:   checksum(src),
			noTx:       noTx,
			statements: statements,
		})
	}
	return set, nil
}

// checksum returns the hex SHA-256 of src with CRLF line endings read as LF,
// so a checkout that converts line endings does not change it.
func checksum(src string) string {
	sum := sha256.Sum256([]byte(strings.ReplaceAll(src, "\r\n", "\n")))
	return hex.EncodeToString(sum[:])
}

// Migration file directives.
const (
	directiveNoTx  = "no-transaction"
	directiveBegin = "begin-statement"
	directiveEnd   = "end-statement"
)

// splitter splits a migration into statements the way dialect d reads SQL.
type splitter struct {
	src   string
	d     Dialect
	i     int // next byte
	line  int // line of byte i
	start int // first byte of the current statement
	code  int // line where the current statement's code starts, or 0
	noTx  bool
	stmts []statement
}

// splitMigration returns a migration's statements and whether it asked to
// run without a transaction. Statements end at semicolons outside quotes and
// comments; a block between begin-statement and end-statement directives is
// one statement, for bodies that contain semicolons.
func splitMigration(src string, d Dialect) (noTx bool, stmts []statement, err error) {
	s := &splitter{src: src, d: d, line: 1}
	for s.i < len(s.src) {
		if s.i == 0 || s.src[s.i-1] == '\n' {
			if name, ok := s.directive(); ok {
				if err := s.apply(name); err != nil {
					return false, nil, err
				}
				s.start = s.i
				continue
			}
		}
		s.token()
	}
	s.emit(len(s.src))
	return s.noTx, s.stmts, nil
}

// directive reports the directive on the line at s.i: a line comment whose
// text starts with "rdb:".
func (s *splitter) directive() (string, bool) {
	line := strings.TrimSpace(s.src[s.i:s.lineEnd(s.i)])
	rest, ok := strings.CutPrefix(line, "--")
	if !ok {
		return "", false
	}
	name, ok := strings.CutPrefix(strings.TrimSpace(rest), "rdb:")
	return strings.TrimSpace(name), ok
}

// apply handles the directive name on the line at s.i and moves past it.
func (s *splitter) apply(name string) error {
	if s.code != 0 {
		return fmt.Errorf("line %d: directive %q inside the statement that starts on line %d", s.line, name, s.code)
	}
	at := s.line
	s.nextLine()
	switch name {
	case directiveNoTx:
		if s.noTx || len(s.stmts) > 0 {
			return fmt.Errorf("line %d: %s must come once, before the first statement", at, directiveNoTx)
		}
		s.noTx = true
	case directiveBegin:
		return s.block(at)
	case directiveEnd:
		return fmt.Errorf("line %d: %s without %s", at, directiveEnd, directiveBegin)
	default:
		return fmt.Errorf("line %d: unknown directive %q", at, name)
	}
	return nil
}

// block reads the statement between a begin-statement directive on line at
// and the matching end-statement directive.
func (s *splitter) block(at int) error {
	bodyStart, bodyLine := s.i, s.line
	for s.i < len(s.src) {
		if name, ok := s.directive(); ok {
			if name != directiveEnd {
				return fmt.Errorf("line %d: directive %q inside the block that starts on line %d", s.line, name, at)
			}
			body := s.src[bodyStart:s.i]
			s.nextLine()
			trimmed := strings.TrimLeft(body, " \t\r\n")
			if strings.TrimSpace(trimmed) == "" {
				return fmt.Errorf("line %d: empty %s block", at, directiveBegin)
			}
			line := bodyLine + strings.Count(body[:len(body)-len(trimmed)], "\n")
			s.stmts = append(s.stmts, statement{sql: strings.TrimSpace(trimmed), line: line})
			return nil
		}
		s.nextLine()
	}
	return fmt.Errorf("line %d: %s without %s", at, directiveBegin, directiveEnd)
}

// token consumes the token at s.i: whitespace, a comment, a quoted string or
// identifier, a statement-ending semicolon, or one byte of code.
func (s *splitter) token() {
	src, i := s.src, s.i
	c := src[i]
	end := i + 1
	code := true
	switch {
	case c == ';':
		s.emit(i)
		s.advance(end)
		s.start = end
		return
	case c == ' ' || c == '\t' || c == '\r' || c == '\n' || c == '\f' || c == '\v':
		code = false
	case c == '-' && s.lineComment(i):
		end, code = s.lineEnd(i), false
	case c == '#' && s.d == MySQL:
		end, code = s.lineEnd(i), false
	case c == '/' && strings.HasPrefix(src[i:], "/*"):
		end, code = s.blockCommentEnd(i), false
	case c == '\'':
		end = quoteEnd(src, i, '\'', s.d == MySQL || (s.d == Postgres && isEscapeString(src, i)))
	case c == '"':
		end = quoteEnd(src, i, '"', s.d == MySQL)
	case c == '`' && s.d != Postgres:
		end = quoteEnd(src, i, '`', false)
	case c == '[' && s.d == SQLite:
		end = bracketEnd(src, i)
	case c == '$' && s.d == Postgres:
		end = dollarQuoteEnd(src, i)
	}
	if code && s.code == 0 {
		s.code = s.line
	}
	s.advance(end)
}

// lineComment reports whether a -- line comment starts at i. MySQL needs
// whitespace, a control character, or the end of input after the dashes.
func (s *splitter) lineComment(i int) bool {
	if !strings.HasPrefix(s.src[i:], "--") {
		return false
	}
	return s.d != MySQL || i+2 == len(s.src) || s.src[i+2] <= ' '
}

// blockCommentEnd returns the index just past the /* comment at i. Only
// Postgres nests block comments.
func (s *splitter) blockCommentEnd(i int) int {
	if s.d == Postgres {
		return blockCommentEnd(s.src, i)
	}
	if j := strings.Index(s.src[i+2:], "*/"); j >= 0 {
		return i + 2 + j + 2
	}
	return len(s.src)
}

// bracketEnd returns the index just past the [identifier] at i.
func bracketEnd(src string, i int) int {
	if j := strings.IndexByte(src[i:], ']'); j >= 0 {
		return i + j + 1
	}
	return len(src)
}

// lineEnd returns the index of the newline that ends the line holding i, or
// the end of input.
func (s *splitter) lineEnd(i int) int {
	if j := strings.IndexByte(s.src[i:], '\n'); j >= 0 {
		return i + j
	}
	return len(s.src)
}

// nextLine moves s.i to the start of the next line.
func (s *splitter) nextLine() {
	end := s.lineEnd(s.i)
	if end < len(s.src) {
		end++
	}
	s.advance(end)
}

// advance moves s.i to end, counting the lines it passes.
func (s *splitter) advance(end int) {
	s.line += strings.Count(s.src[s.i:end], "\n")
	s.i = end
}

// emit ends the current statement at end, keeping it when it holds code.
func (s *splitter) emit(end int) {
	if s.code != 0 {
		s.stmts = append(s.stmts, statement{sql: strings.TrimSpace(s.src[s.start:end]), line: s.code})
	}
	s.code = 0
	s.start = end
}
