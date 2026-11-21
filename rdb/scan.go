package rdb

import (
	"context"
	"database/sql"
	"fmt"
	"iter"
	"reflect"
	"strings"
	"sync"
	"time"
)

// One runs query and scans its first row into a T. It returns ErrNotFound
// when there are no rows and ignores rows after the first.
//
// T is either a struct, whose fields receive columns by name, or a single
// value such as int, string, time.Time, a pointer for a nullable column, or
// a sql.Scanner, which receives the only column. Struct fields match a
// column by their db tag ignoring case, or, untagged, by their name ignoring
// case and underscores, so CreatedAt matches created_at. A field tagged
// db:"-" is skipped, and embedded structs are flattened. Unless the
// Querier's DB was opened with WithoutStrictScan, a column with no matching
// field fails with ErrScan; a field with no column keeps its zero value.
func One[T any](ctx context.Context, q Querier, query string, args ...any) (T, error) {
	var zero T
	rows, err := q.Query(ctx, query, args...)
	if err != nil {
		return zero, err
	}
	defer rows.Close()
	s, err := newScanner[T](rows, strictOf(q))
	if err != nil {
		return zero, err
	}
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return zero, wrapRows(q, err)
		}
		return zero, &Error{Op: "query", Kind: ErrNotFound, Err: sql.ErrNoRows}
	}
	v, err := s.scan(rows)
	if err != nil {
		return zero, err
	}
	return v, wrapRows(q, rows.Close())
}

// All runs query and scans every row into a T, as One describes. It returns
// an empty, non-nil slice when there are no rows.
func All[T any](ctx context.Context, q Querier, query string, args ...any) ([]T, error) {
	out := []T{}
	for v, err := range Each[T](ctx, q, query, args...) {
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// Each runs query when iterated and yields every row scanned into a T, as
// One describes. On failure it yields the zero T and the error, then stops.
// Breaking out of the loop closes the rows.
func Each[T any](ctx context.Context, q Querier, query string, args ...any) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		var zero T
		rows, err := q.Query(ctx, query, args...)
		if err != nil {
			yield(zero, err)
			return
		}
		defer rows.Close()
		s, err := newScanner[T](rows, strictOf(q))
		if err != nil {
			yield(zero, err)
			return
		}
		for rows.Next() {
			v, err := s.scan(rows)
			if err != nil {
				yield(zero, err)
				return
			}
			if !yield(v, nil) {
				return
			}
		}
		if err := rows.Err(); err != nil {
			yield(zero, wrapRows(q, err))
		}
	}
}

// strictOf reports whether q rejects unmatched columns. Queriers from
// outside the package scan strictly.
func strictOf(q Querier) bool {
	if s, ok := q.(interface{ strictScan() bool }); ok {
		return s.strictScan()
	}
	return true
}

// wrapRows classifies an error from iterating rows through q's driver when q
// comes from this package.
func wrapRows(q Querier, err error) error {
	if w, ok := q.(interface{ wrap(string, error) error }); ok {
		return w.wrap("query", err)
	}
	return err
}

// scanner scans rows into values of type T.
type scanner[T any] struct {
	// fields holds, for each column, the index path of its struct field, or
	// nil when the column is discarded. It is nil for a single-value T.
	fields [][]int
}

var (
	scannerType = reflect.TypeFor[sql.Scanner]()
	timeType    = reflect.TypeFor[time.Time]()
)

// newScanner maps the columns of rows onto T.
func newScanner[T any](rows *sql.Rows, strict bool) (*scanner[T], error) {
	cols, err := rows.Columns()
	if err != nil {
		return nil, &Error{Op: "scan", Kind: ErrScan, Err: err}
	}
	t := reflect.TypeFor[T]()
	if !isStruct(t) {
		if len(cols) != 1 {
			return nil, scanError("%s needs 1 column, got %d", t, len(cols))
		}
		return &scanner[T]{}, nil
	}
	fields := fieldsOf(t)
	s := &scanner[T]{fields: make([][]int, len(cols))}
	used := make(map[string]string, len(cols))
	for i, col := range cols {
		f, ok, err := fields.match(col)
		if err != nil {
			return nil, err
		}
		if !ok {
			if strict {
				return nil, scanError("column %q has no field in %s", col, t)
			}
			continue
		}
		if prev, ok := used[f.name]; ok {
			return nil, scanError("columns %q and %q both map to field %s", prev, col, f.name)
		}
		used[f.name] = col
		s.fields[i] = f.index
	}
	return s, nil
}

// scan scans the current row into a new T.
func (s *scanner[T]) scan(rows *sql.Rows) (T, error) {
	var v T
	var dest []any
	if s.fields == nil {
		dest = []any{&v}
	} else {
		rv := reflect.ValueOf(&v).Elem()
		dest = make([]any, len(s.fields))
		for i, index := range s.fields {
			if index == nil {
				dest[i] = new(any)
				continue
			}
			dest[i] = rv.FieldByIndex(index).Addr().Interface()
		}
	}
	if err := rows.Scan(dest...); err != nil {
		return v, &Error{Op: "scan", Kind: ErrScan, Err: err}
	}
	return v, nil
}

// scanError returns an *Error of kind ErrScan.
func scanError(format string, args ...any) error {
	return &Error{Op: "scan", Kind: ErrScan, Err: fmt.Errorf(format, args...)}
}

// isStruct reports whether t is scanned field by field rather than as one
// value.
func isStruct(t reflect.Type) bool {
	return t.Kind() == reflect.Struct && t != timeType && !reflect.PointerTo(t).Implements(scannerType)
}

// field is a struct field that can receive a column.
type field struct {
	name   string // Go field path, for messages, such as "Audit.CreatedBy"
	tag    string // lowercased db tag, or "" when untagged
	key    string // name lowercased with underscores removed
	index  []int
	tagged bool
}

// fieldSet is the scannable fields of a struct type.
type fieldSet []field

// fieldCache maps a struct type to its fieldSet.
var fieldCache sync.Map

// fieldsOf returns the scannable fields of struct type t.
func fieldsOf(t reflect.Type) fieldSet {
	if fs, ok := fieldCache.Load(t); ok {
		return fs.(fieldSet)
	}
	var fs fieldSet
	collectFields(t, nil, "", &fs)
	fieldCache.Store(t, fs)
	return fs
}

// collectFields appends the fields of t, flattening untagged embedded
// structs, with index paths under parent.
func collectFields(t reflect.Type, parent []int, prefix string, fs *fieldSet) {
	for i := range t.NumField() {
		sf := t.Field(i)
		tag := sf.Tag.Get("db")
		if tag == "-" {
			continue
		}
		index := append(append([]int(nil), parent...), i)
		if sf.Anonymous && tag == "" && isStruct(sf.Type) {
			collectFields(sf.Type, index, prefix+sf.Name+".", fs)
			continue
		}
		if !sf.IsExported() {
			continue
		}
		*fs = append(*fs, field{
			name:   prefix + sf.Name,
			tag:    strings.ToLower(tag),
			key:    normalize(sf.Name),
			index:  index,
			tagged: tag != "",
		})
	}
}

// match returns the field for column col: the field tagged col ignoring
// case, or else the untagged field whose name matches col ignoring case and
// underscores. It reports false when none matches, and ErrScan when two
// fields match equally.
func (fs fieldSet) match(col string) (*field, bool, error) {
	lower := strings.ToLower(col)
	key := normalize(col)
	var found *field
	for _, byTag := range []bool{true, false} {
		for i := range fs {
			f := &fs[i]
			if f.tagged != byTag {
				continue
			}
			if (byTag && f.tag != lower) || (!byTag && f.key != key) {
				continue
			}
			if found != nil {
				return nil, false, scanError("column %q matches fields %s and %s", col, found.name, f.name)
			}
			found = f
		}
		if found != nil {
			return found, true, nil
		}
	}
	return nil, false, nil
}

// normalize lowercases s and removes underscores.
func normalize(s string) string {
	return strings.ReplaceAll(strings.ToLower(s), "_", "")
}
