package objex

import (
	"context"
	"errors"
	"io"
	"iter"
	"slices"
	"testing"
)

// recordingBucket implements Bucket with scripted Stat, Copy, and Delete
// results so the helpers can be tested without a driver.
type recordingBucket struct {
	statErr   error
	copyErr   error
	deleteErr error
	calls     []string
}

func (b *recordingBucket) Put(context.Context, string, io.Reader, ...PutOption) (ObjectInfo, error) {
	return ObjectInfo{}, nil
}

func (b *recordingBucket) Get(context.Context, string, ...GetOption) (*Object, error) {
	return nil, nil
}

func (b *recordingBucket) Stat(_ context.Context, key string) (ObjectInfo, error) {
	b.calls = append(b.calls, "stat "+key)
	return ObjectInfo{Key: key}, b.statErr
}

func (b *recordingBucket) Delete(_ context.Context, key string) error {
	b.calls = append(b.calls, "delete "+key)
	return b.deleteErr
}

func (b *recordingBucket) List(context.Context, ...ListOption) iter.Seq2[ObjectInfo, error] {
	return func(func(ObjectInfo, error) bool) {}
}

func (b *recordingBucket) Copy(_ context.Context, dst, src string, _ ...CopyOption) (ObjectInfo, error) {
	b.calls = append(b.calls, "copy "+src+" "+dst)
	return ObjectInfo{Key: dst}, b.copyErr
}

type movingBucket struct{ recordingBucket }

func (b *movingBucket) Move(_ context.Context, dst, src string) (ObjectInfo, error) {
	b.calls = append(b.calls, "move "+src+" "+dst)
	return ObjectInfo{Key: dst}, nil
}

func TestExists(t *testing.T) {
	ctx := context.Background()
	if ok, err := Exists(ctx, &recordingBucket{}, "k"); !ok || err != nil {
		t.Errorf("Exists(present) = %v, %v", ok, err)
	}
	missing := &recordingBucket{statErr: NewError("stat", "b", "k", ErrNotFound, nil)}
	if ok, err := Exists(ctx, missing, "k"); ok || err != nil {
		t.Errorf("Exists(missing) = %v, %v", ok, err)
	}
	failing := &recordingBucket{statErr: ErrPermission}
	if ok, err := Exists(ctx, failing, "k"); ok || !errors.Is(err, ErrPermission) {
		t.Errorf("Exists(failing) = %v, %v", ok, err)
	}
}

func TestMove(t *testing.T) {
	ctx := context.Background()

	b := &recordingBucket{}
	info, err := Move(ctx, b, "dst", "src")
	if err != nil || info.Key != "dst" {
		t.Fatalf("Move = %+v, %v", info, err)
	}
	if want := []string{"copy src dst", "delete src"}; !slices.Equal(b.calls, want) {
		t.Errorf("calls = %q, want %q", b.calls, want)
	}

	native := &movingBucket{}
	if _, err := Move(ctx, native, "dst", "src"); err != nil {
		t.Fatalf("Move with Mover: %v", err)
	}
	if want := []string{"move src dst"}; !slices.Equal(native.calls, want) {
		t.Errorf("calls = %q, want %q", native.calls, want)
	}

	same := &recordingBucket{}
	if info, err := Move(ctx, same, "k", "k"); err != nil || info.Key != "k" {
		t.Errorf("Move onto itself = %+v, %v", info, err)
	}
	if want := []string{"stat k"}; !slices.Equal(same.calls, want) {
		t.Errorf("calls = %q, want %q", same.calls, want)
	}
	if _, err := Move(ctx, same, "/bad", "/bad"); !errors.Is(err, ErrInvalidKey) {
		t.Errorf("Move invalid key onto itself: err = %v, want ErrInvalidKey", err)
	}

	copyFails := &recordingBucket{copyErr: ErrNotFound}
	if _, err := Move(ctx, copyFails, "dst", "src"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Move copy failure: err = %v", err)
	}
	if slices.Contains(copyFails.calls, "delete src") {
		t.Error("Move deleted the source after Copy failed")
	}

	deleteFails := &recordingBucket{deleteErr: ErrPermission}
	if _, err := Move(ctx, deleteFails, "dst", "src"); !errors.Is(err, ErrPermission) {
		t.Errorf("Move delete failure: err = %v", err)
	}
}

func TestFilterList(t *testing.T) {
	entries := []ObjectInfo{
		{Key: "c.txt"}, {Key: "b/2.txt"}, {Key: "a.txt"}, {Key: "b/1.txt"},
		{Key: "b/c/3.txt"}, {Key: "b-sibling.txt"},
	}
	keys := func(infos []ObjectInfo) []string {
		out := make([]string, 0, len(infos))
		for _, info := range infos {
			key := info.Key
			if info.IsPrefix {
				key += "*"
			}
			out = append(out, key)
		}
		return out
	}

	cases := []struct {
		opts ListOptions
		want []string
	}{
		{ListOptions{}, []string{"a.txt", "b-sibling.txt", "b/1.txt", "b/2.txt", "b/c/3.txt", "c.txt"}},
		{ListOptions{Prefix: "b/"}, []string{"b/1.txt", "b/2.txt", "b/c/3.txt"}},
		{ListOptions{StartAfter: "b/2.txt"}, []string{"b/c/3.txt", "c.txt"}},
		{ListOptions{Delimiter: "/"}, []string{"a.txt", "b-sibling.txt", "b/*", "c.txt"}},
		{ListOptions{Prefix: "b/", Delimiter: "/"}, []string{"b/1.txt", "b/2.txt", "b/c/*"}},
		{ListOptions{Prefix: "missing/"}, []string{}},
	}
	for _, c := range cases {
		if got := keys(FilterList(entries, c.opts)); !slices.Equal(got, c.want) {
			t.Errorf("FilterList(%+v) = %q, want %q", c.opts, got, c.want)
		}
	}
}
