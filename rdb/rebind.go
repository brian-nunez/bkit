package rdb

import (
	"strconv"
	"strings"
)

// rebindDollar rewrites ? placeholders as $1, $2, ... in Postgres syntax. It
// leaves ? alone inside string constants (including E'...' escape strings and
// $tag$ dollar-quoted strings), quoted identifiers, and comments. Text after
// an unterminated quote or comment is copied unchanged.
func rebindDollar(query string) string {
	if !strings.Contains(query, "?") {
		return query
	}
	var b strings.Builder
	b.Grow(len(query) + 8)
	n := 0
	for i := 0; i < len(query); {
		c := query[i]
		end := i + 1
		switch {
		case c == '?':
			n++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))
			i = end
			continue
		case c == '\'':
			end = quoteEnd(query, i, '\'', isEscapeString(query, i))
		case c == '"':
			end = quoteEnd(query, i, '"', false)
		case c == '-' && strings.HasPrefix(query[i:], "--"):
			end = lineCommentEnd(query, i)
		case c == '/' && strings.HasPrefix(query[i:], "/*"):
			end = blockCommentEnd(query, i)
		case c == '$':
			end = dollarQuoteEnd(query, i)
		}
		b.WriteString(query[i:end])
		i = end
	}
	return b.String()
}

// isEscapeString reports whether the quote at i opens an E'...' string, in
// which a backslash escapes the next character.
func isEscapeString(query string, i int) bool {
	if i == 0 || (query[i-1] != 'e' && query[i-1] != 'E') {
		return false
	}
	return i == 1 || !isIdentByte(query[i-2])
}

// quoteEnd returns the index just past the quoted text that starts at i. A
// doubled quote character stands for itself; with backslash set, a
// backslash escapes the next character.
func quoteEnd(query string, i int, quote byte, backslash bool) int {
	for j := i + 1; j < len(query); j++ {
		switch query[j] {
		case '\\':
			if backslash {
				j++
			}
		case quote:
			if j+1 < len(query) && query[j+1] == quote {
				j++
				continue
			}
			return j + 1
		}
	}
	return len(query)
}

// lineCommentEnd returns the index just past the -- comment at i, including
// its newline.
func lineCommentEnd(query string, i int) int {
	if j := strings.IndexByte(query[i:], '\n'); j >= 0 {
		return i + j + 1
	}
	return len(query)
}

// blockCommentEnd returns the index just past the /* comment at i. Postgres
// block comments nest.
func blockCommentEnd(query string, i int) int {
	depth := 0
	for j := i; j+1 < len(query); j++ {
		switch query[j : j+2] {
		case "/*":
			depth++
			j++
		case "*/":
			depth--
			j++
			if depth == 0 {
				return j + 1
			}
		}
	}
	return len(query)
}

// dollarQuoteEnd returns the index just past the dollar-quoted string at i,
// or i+1 when the $ does not open one, as in a $1 parameter or an
// identifier such as a$b, since an identifier may contain $.
func dollarQuoteEnd(query string, i int) int {
	if i > 0 && (isIdentByte(query[i-1]) || query[i-1] == '$') {
		return i + 1
	}
	j := i + 1
	for j < len(query) && isIdentByte(query[j]) && (j > i+1 || !isDigit(query[j])) {
		j++
	}
	if j >= len(query) || query[j] != '$' {
		return i + 1
	}
	tag := query[i : j+1]
	if k := strings.Index(query[j+1:], tag); k >= 0 {
		return j + 1 + k + len(tag)
	}
	return len(query)
}

// isIdentByte reports whether b can appear in an unquoted identifier.
func isIdentByte(b byte) bool {
	return b == '_' || isDigit(b) || b >= 0x80 || (b|0x20 >= 'a' && b|0x20 <= 'z')
}

// isDigit reports whether b is an ASCII digit.
func isDigit(b byte) bool {
	return b >= '0' && b <= '9'
}
