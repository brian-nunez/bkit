package objex

import (
	"errors"
	"io/fs"
	"testing"
)

func TestErrorMatchesKindAndCause(t *testing.T) {
	cause := fs.ErrNotExist
	err := error(NewError("get", "assets", "images/logo.png", ErrNotFound, cause))

	if !errors.Is(err, ErrNotFound) {
		t.Error("errors.Is(err, ErrNotFound) = false")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Error("errors.Is(err, fs.ErrNotExist) = false")
	}
	if errors.Is(err, ErrPermission) {
		t.Error("errors.Is(err, ErrPermission) = true")
	}

	var objexErr *Error
	if !errors.As(err, &objexErr) || objexErr.Op != "get" || objexErr.Key != "images/logo.png" {
		t.Errorf("errors.As = %+v", objexErr)
	}
}

func TestErrorMessage(t *testing.T) {
	cases := []struct {
		err  *Error
		want string
	}{
		{NewError("get", "assets", "a/b", ErrNotFound, errors.New("NoSuchKey")), "objex: get assets/a/b: object not found: NoSuchKey"},
		{NewError("create bucket", "assets", "", ErrBucketExists, nil), "objex: create bucket assets: bucket already exists"},
		{NewError("list", "", "", nil, errors.New("timeout")), "objex: list: timeout"},
		{NewError("put", "", "key", ErrInvalidKey, nil), "objex: put key: invalid key"},
	}
	for _, c := range cases {
		if got := c.err.Error(); got != c.want {
			t.Errorf("Error() = %q, want %q", got, c.want)
		}
	}
}

func TestErrorUnwrapOmitsNil(t *testing.T) {
	if got := NewError("op", "", "", nil, nil).Unwrap(); len(got) != 0 {
		t.Errorf("Unwrap() = %v, want empty", got)
	}
	if got := NewError("op", "", "", ErrNotFound, nil).Unwrap(); len(got) != 1 || !errors.Is(got[0], ErrNotFound) {
		t.Errorf("Unwrap() = %v, want [ErrNotFound]", got)
	}
}
