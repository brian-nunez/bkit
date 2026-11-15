package stash

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"time"
)

// Codec converts values to and from bytes for a TypedStore.
type Codec interface {
	// Marshal encodes v.
	Marshal(v any) ([]byte, error)

	// Unmarshal decodes data into the value v points to.
	Unmarshal(data []byte, v any) error
}

// JSON encodes values with encoding/json.
var JSON Codec = jsonCodec{}

// jsonCodec implements Codec with encoding/json.
type jsonCodec struct{}

// Marshal encodes v as JSON.
func (jsonCodec) Marshal(v any) ([]byte, error) {
	return json.Marshal(v)
}

// Unmarshal decodes JSON data into v.
func (jsonCodec) Unmarshal(data []byte, v any) error {
	return json.Unmarshal(data, v)
}

// TypedStore stores values of type T, encoded by a Codec, in a Store. The
// codec runs before values reach the driver, so a TypedStore behaves the
// same on every driver. Create one with Typed; it is safe for concurrent
// use.
type TypedStore[T any] struct {
	store *Store
	codec Codec
}

// Typed returns a TypedStore that keeps values of type T in s, encoded with
// codec.
func Typed[T any](s *Store, codec Codec) *TypedStore[T] {
	return &TypedStore[T]{store: s, codec: codec}
}

// Store returns the underlying Store.
func (t *TypedStore[T]) Store() *Store {
	return t.store
}

// Get returns the value of key, or ErrNotFound. A stored value the codec
// cannot decode returns ErrCodec.
func (t *TypedStore[T]) Get(ctx context.Context, key string) (T, error) {
	var v T
	data, err := t.store.Get(ctx, key)
	if err != nil {
		return v, err
	}
	if err := t.codec.Unmarshal(data, &v); err != nil {
		return v, &Error{Op: "get", Key: key, Err: codecError(err)}
	}
	return v, nil
}

// Set encodes v and stores it under key, as Store.Set does. A value the
// codec cannot encode returns ErrCodec.
func (t *TypedStore[T]) Set(ctx context.Context, key string, v T, opts ...SetOption) error {
	data, err := t.codec.Marshal(v)
	if err != nil {
		return &Error{Op: "set", Key: key, Err: codecError(err)}
	}
	return t.store.Set(ctx, key, data, opts...)
}

// Delete removes key and reports whether it existed.
func (t *TypedStore[T]) Delete(ctx context.Context, key string) (bool, error) {
	return t.store.Delete(ctx, key)
}

// Exists reports whether key is set.
func (t *TypedStore[T]) Exists(ctx context.Context, key string) (bool, error) {
	return t.store.Exists(ctx, key)
}

// TTL returns the time left before key expires, as Store.TTL does.
func (t *TypedStore[T]) TTL(ctx context.Context, key string) (time.Duration, error) {
	return t.store.TTL(ctx, key)
}

// Expire makes key expire after ttl, as Store.Expire does.
func (t *TypedStore[T]) Expire(ctx context.Context, key string, ttl time.Duration) error {
	return t.store.Expire(ctx, key, ttl)
}

// Keys yields the keys in the store, as Store.Keys does.
func (t *TypedStore[T]) Keys(ctx context.Context, opts ...KeysOption) iter.Seq2[string, error] {
	return t.store.Keys(ctx, opts...)
}

// codecError wraps a codec failure so it matches ErrCodec and the codec's
// own error.
func codecError(err error) error {
	return fmt.Errorf("%w: %w", ErrCodec, err)
}
