package objex

import (
	"errors"
	"testing"
)

func TestResolvePutOptions(t *testing.T) {
	o, err := ResolvePutOptions()
	if err != nil {
		t.Fatalf("ResolvePutOptions() error = %v", err)
	}
	if o.Size != -1 || o.ContentType != "" || o.Metadata != nil || o.IfMatch != "" || o.IfNotExists {
		t.Errorf("default PutOptions = %+v", o)
	}

	source := map[string]string{"Owner": "objex"}
	o, err = ResolvePutOptions(
		WithContentType("text/plain"),
		WithMetadata(source),
		WithSize(12),
		IfMatch(`"abc"`),
		nil,
	)
	if err != nil {
		t.Fatalf("ResolvePutOptions error = %v", err)
	}
	if o.ContentType != "text/plain" || o.Size != 12 || o.IfMatch != "abc" {
		t.Errorf("PutOptions = %+v", o)
	}
	if o.Metadata["owner"] != "objex" || len(o.Metadata) != 1 {
		t.Errorf("Metadata = %v, want lowercase owner key", o.Metadata)
	}
	source["Owner"] = "changed"
	if o.Metadata["owner"] != "objex" {
		t.Error("Metadata aliases the caller's map")
	}

	if o, err := ResolvePutOptions(IfNotExists()); err != nil || !o.IfNotExists {
		t.Errorf("IfNotExists = %+v, %v", o, err)
	}

	invalid := [][]PutOption{
		{WithSize(-2)},
		{IfNotExists(), IfMatch("etag")},
		{WithMetadata(map[string]string{"": "v"})},
		{WithMetadata(map[string]string{"has space": "v"})},
		{WithMetadata(map[string]string{"under_score": "v"})},
		{WithMetadata(map[string]string{"k": "line\nbreak"})},
		{WithMetadata(map[string]string{"k": "non-ascii é"})},
		{WithMetadata(map[string]string{"Key": "a", "key": "b"})},
	}
	for i, opts := range invalid {
		if _, err := ResolvePutOptions(opts...); !errors.Is(err, ErrInvalidOption) {
			t.Errorf("case %d: err = %v, want ErrInvalidOption", i, err)
		}
	}
}

func TestResolveGetOptions(t *testing.T) {
	o, err := ResolveGetOptions()
	if err != nil || o.Range != nil || o.IfMatch != "" {
		t.Fatalf("default GetOptions = %+v, %v", o, err)
	}

	o, err = ResolveGetOptions(WithRange(5, -1), IfMatch("etag"))
	if err != nil {
		t.Fatalf("ResolveGetOptions error = %v", err)
	}
	if o.Range == nil || o.Range.Offset != 5 || o.Range.Length != -1 || o.IfMatch != "etag" {
		t.Errorf("GetOptions = %+v", o)
	}

	for _, r := range []RangeOption{WithRange(-1, 4), WithRange(0, 0)} {
		if _, err := ResolveGetOptions(r); !errors.Is(err, ErrInvalidRange) {
			t.Errorf("WithRange(%+v): err = %v, want ErrInvalidRange", r.r, err)
		}
	}
}

func TestResolveListOptions(t *testing.T) {
	o := ResolveListOptions(WithPrefix("p/"), WithDelimiter("/"), WithStartAfter("p/a"), nil)
	if o.Prefix != "p/" || o.Delimiter != "/" || o.StartAfter != "p/a" {
		t.Errorf("ListOptions = %+v", o)
	}
}

func TestResolveCopyOptions(t *testing.T) {
	o, err := ResolveCopyOptions()
	if err != nil || o.ReplaceMetadata || o.ContentType != "" {
		t.Fatalf("default CopyOptions = %+v, %v", o, err)
	}

	o, err = ResolveCopyOptions(WithContentType("text/csv"), WithMetadata(map[string]string{"Stage": "copied"}), IfMatch("etag"))
	if err != nil {
		t.Fatalf("ResolveCopyOptions error = %v", err)
	}
	if o.ContentType != "text/csv" || !o.ReplaceMetadata || o.Metadata["stage"] != "copied" || o.IfMatch != "etag" {
		t.Errorf("CopyOptions = %+v", o)
	}

	o, err = ResolveCopyOptions(WithMetadata(nil))
	if err != nil || !o.ReplaceMetadata || len(o.Metadata) != 0 {
		t.Errorf("WithMetadata(nil) = %+v, %v; want ReplaceMetadata with no metadata", o, err)
	}

	if _, err := ResolveCopyOptions(WithMetadata(map[string]string{"bad key": "v"})); !errors.Is(err, ErrInvalidOption) {
		t.Errorf("invalid metadata: err = %v, want ErrInvalidOption", err)
	}
}
