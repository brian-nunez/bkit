package objex

import (
	"context"
	"errors"
	"slices"
	"strings"
)

// Exists reports whether an object is stored under key in b.
func Exists(ctx context.Context, b Bucket, key string) (bool, error) {
	_, err := b.Stat(ctx, key)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, ErrNotFound):
		return false, nil
	default:
		return false, err
	}
}

// Move moves the object at src to dst within b. It uses the bucket's native
// move when b implements Mover; otherwise it copies src to dst, then deletes
// src. The fallback is not atomic: if the delete fails, both objects remain
// and the error is returned.
func Move(ctx context.Context, b Bucket, dst, src string) (ObjectInfo, error) {
	if mover, ok := b.(Mover); ok {
		return mover.Move(ctx, dst, src)
	}
	if dst == src {
		if err := ValidateKey(src); err != nil {
			return ObjectInfo{}, NewError("move", "", src, ErrInvalidKey, err)
		}
		return b.Stat(ctx, src)
	}
	info, err := b.Copy(ctx, dst, src)
	if err != nil {
		return ObjectInfo{}, err
	}
	if err := b.Delete(ctx, src); err != nil {
		return ObjectInfo{}, err
	}
	return info, nil
}

// FilterList applies prefix, start-after, and delimiter semantics to entries
// a driver has collected, returning them in ascending key order. Drivers
// whose backend lists everything at once (memory, filesystem) use it so every
// driver groups and orders results identically.
func FilterList(entries []ObjectInfo, opts ListOptions) []ObjectInfo {
	selected := make([]ObjectInfo, 0, len(entries))
	for _, entry := range entries {
		if strings.HasPrefix(entry.Key, opts.Prefix) && entry.Key > opts.StartAfter {
			selected = append(selected, entry)
		}
	}
	slices.SortFunc(selected, func(a, b ObjectInfo) int { return strings.Compare(a.Key, b.Key) })

	if opts.Delimiter == "" {
		return selected
	}

	out := make([]ObjectInfo, 0, len(selected))
	lastPrefix := ""
	for _, entry := range selected {
		rest := entry.Key[len(opts.Prefix):]
		i := strings.Index(rest, opts.Delimiter)
		if i < 0 {
			out = append(out, entry)
			continue
		}
		prefix := opts.Prefix + rest[:i+len(opts.Delimiter)]
		if prefix == lastPrefix {
			continue
		}
		lastPrefix = prefix
		out = append(out, ObjectInfo{Key: prefix, IsPrefix: true})
	}
	return out
}
