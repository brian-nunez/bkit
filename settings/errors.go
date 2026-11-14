package settings

import (
	"errors"
	"strings"
)

// Sentinel errors. They arrive wrapped in *Error; match them with errors.Is.
var (
	// ErrNotFound reports that no value exists at a path.
	ErrNotFound = errors.New("settings: not found")

	// ErrType reports a value that cannot convert to the requested type.
	ErrType = errors.New("settings: type mismatch")

	// ErrParse reports a document its Format could not parse.
	ErrParse = errors.New("settings: parse failed")
)

// Error describes a failure to load or read a value. It matches its Err, and
// any sentinel Err wraps, with errors.Is and errors.As.
type Error struct {
	// Source names the source that failed, if any.
	Source string

	// Path is the lowercased key path involved, if any.
	Path string

	// Err is the cause: a sentinel, a context error, or a source's error.
	Err error
}

// Error formats the source, path, and cause.
func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString("settings: ")
	if e.Source != "" {
		b.WriteString("source ")
		b.WriteString(e.Source)
		b.WriteString(": ")
	}
	if e.Path != "" {
		b.WriteString("key ")
		b.WriteString(e.Path)
		b.WriteString(": ")
	}
	b.WriteString(strings.TrimPrefix(e.Err.Error(), "settings: "))
	return b.String()
}

// Unwrap returns Err.
func (e *Error) Unwrap() error {
	return e.Err
}
