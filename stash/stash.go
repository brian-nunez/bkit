package stash

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// MaxKeyLen is the longest full key, namespace included, in bytes.
const MaxKeyLen = 512

// NoExpiry is the TTL of a key that never expires.
const NoExpiry time.Duration = -1

// separator joins a namespace to the keys inside it.
const separator = ":"

// Backend stores keys for one kind of store. Driver modules implement it;
// applications use a Store, which validates keys, applies namespaces, and
// wraps errors before calling the Backend.
//
// Every method receives full keys, namespace included. A Backend must treat
// an expired key as missing, return ErrNotFound, ErrExists, and ErrNotInteger
// (wrapped or not) for those conditions, honor ctx, and be safe for
// concurrent use.
type Backend interface {
	// Get returns the value of key, or ErrNotFound.
	Get(ctx context.Context, key string) ([]byte, error)

	// Set stores value under key. With opts.IfNotExists it stores nothing
	// and returns ErrExists when key is set. A zero opts.TTL means the key
	// never expires.
	Set(ctx context.Context, key string, value []byte, opts SetOptions) error

	// Delete removes key and reports whether it existed.
	Delete(ctx context.Context, key string) (bool, error)

	// Exists reports whether key is set.
	Exists(ctx context.Context, key string) (bool, error)

	// Incr adds delta to the base-10 integer stored under key and returns
	// the result, as one atomic step. A missing key counts as 0 and is
	// created without expiry; an existing key keeps its expiry. A value
	// that is not an integer, or a result that overflows int64, returns
	// ErrNotInteger.
	Incr(ctx context.Context, key string, delta int64) (int64, error)

	// TTL returns the time left before key expires, NoExpiry, or
	// ErrNotFound.
	TTL(ctx context.Context, key string) (time.Duration, error)

	// Expire sets key to expire after ttl, or never when ttl is 0. It
	// returns ErrNotFound when key is missing.
	Expire(ctx context.Context, key string, ttl time.Duration) error

	// Keys yields every key that starts with prefix, matched literally.
	// The order is unspecified, and a key may repeat when the store
	// changes during iteration.
	Keys(ctx context.Context, prefix string) iter.Seq2[string, error]

	// DeletePrefix removes every key that starts with prefix, matched
	// literally. prefix is never empty.
	DeletePrefix(ctx context.Context, prefix string) error

	// Ping verifies that the store is reachable.
	Ping(ctx context.Context) error

	// Close releases the Backend's resources.
	Close() error
}

// SetOptions are the options of one Set call, as a Backend receives them.
type SetOptions struct {
	// TTL is how long the key lives. Zero means it never expires.
	TTL time.Duration

	// IfNotExists stores the value only when the key is not set.
	IfNotExists bool
}

// Driver opens Backends for URL schemes. Driver modules implement it;
// applications pass a driver to Open through the module's Driver function.
type Driver interface {
	// Schemes returns the lowercase URL schemes the driver opens.
	Schemes() []string

	// Open returns a Backend for u. Its errors must not contain u or its
	// credentials.
	Open(ctx context.Context, u *url.URL) (Backend, error)
}

// Option configures Open.
type Option func(*options)

// options holds the settings Open collects.
type options struct {
	drivers []Driver
}

// WithDriver makes d available to Open. Open uses the first driver whose
// schemes include the URL's scheme. Callers must not pass a nil Driver.
func WithDriver(d Driver) Option {
	return func(o *options) { o.drivers = append(o.drivers, d) }
}

// Store is a key-value store. Create one with Open or New; the zero value
// is not usable. A Store is safe for concurrent use, and its Namespace
// stores share its Backend.
type Store struct {
	backend Backend
	closed  *atomic.Bool
	prefix  string // full namespace prefix, ending in separator, or ""
	err     error  // set when Namespace was given an invalid name
}

// Open connects to the store rawURL names. Pass the drivers the application
// supports; the URL's scheme picks one, and only that driver connects.
func Open(ctx context.Context, rawURL string, opts ...Option) (*Store, error) {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme == "" {
		// url.Parse errors quote the URL, which may hold credentials.
		return nil, &Error{Op: "open", Err: ErrInvalidURL}
	}
	i := slices.IndexFunc(o.drivers, func(d Driver) bool {
		return slices.ContainsFunc(d.Schemes(), func(s string) bool { return strings.EqualFold(s, u.Scheme) })
	})
	if i < 0 {
		return nil, &Error{Op: "open", Err: fmt.Errorf("%w: scheme %q", ErrUnknownDriver, u.Scheme)}
	}
	b, err := o.drivers[i].Open(ctx, u)
	if err != nil {
		return nil, &Error{Op: "open", Err: err}
	}
	return New(b), nil
}

// New returns a Store over b. Driver modules call it from their typed
// constructors.
func New(b Backend) *Store {
	return &Store{backend: b, closed: new(atomic.Bool)}
}

// Namespace returns a Store whose keys live under name, separated by a
// colon. Namespaces nest, and every namespace store shares s's Backend. A
// name that is empty, not valid UTF-8, or contains a colon makes every call
// on the returned store fail with ErrInvalidKey, so Namespace("a:b") never
// shares keys with Namespace("a").Namespace("b").
func (s *Store) Namespace(name string) *Store {
	ns := *s
	if name == "" || !utf8.ValidString(name) || strings.Contains(name, separator) {
		ns.err = &Error{Op: "namespace", Err: fmt.Errorf("%w: namespace must be non-empty UTF-8 without a colon", ErrInvalidKey)}
	}
	ns.prefix = s.prefix + name + separator
	return &ns
}

// Get returns the value of key, or ErrNotFound.
func (s *Store) Get(ctx context.Context, key string) ([]byte, error) {
	full, err := s.full("get", key)
	if err != nil {
		return nil, err
	}
	v, err := s.backend.Get(ctx, full)
	return v, wrap("get", key, err)
}

// Set stores value under key. WithTTL makes it expire, and IfNotExists
// stores it only when key is not set, returning ErrExists otherwise.
func (s *Store) Set(ctx context.Context, key string, value []byte, opts ...SetOption) error {
	full, err := s.full("set", key)
	if err != nil {
		return err
	}
	var o SetOptions
	for _, opt := range opts {
		opt(&o)
	}
	if o.TTL < 0 {
		return &Error{Op: "set", Key: key, Err: fmt.Errorf("%w: negative TTL", ErrInvalidOption)}
	}
	return wrap("set", key, s.backend.Set(ctx, full, value, o))
}

// Delete removes key and reports whether it existed.
func (s *Store) Delete(ctx context.Context, key string) (bool, error) {
	full, err := s.full("delete", key)
	if err != nil {
		return false, err
	}
	ok, err := s.backend.Delete(ctx, full)
	return ok, wrap("delete", key, err)
}

// Exists reports whether key is set.
func (s *Store) Exists(ctx context.Context, key string) (bool, error) {
	full, err := s.full("exists", key)
	if err != nil {
		return false, err
	}
	ok, err := s.backend.Exists(ctx, full)
	return ok, wrap("exists", key, err)
}

// Incr adds delta to the integer stored under key and returns the result,
// as one atomic step. A missing key counts as 0 and is created without
// expiry. Counters are stored as base-10 text, so Get returns "5". A value
// that is not an integer returns ErrNotInteger.
func (s *Store) Incr(ctx context.Context, key string, delta int64) (int64, error) {
	full, err := s.full("incr", key)
	if err != nil {
		return 0, err
	}
	n, err := s.backend.Incr(ctx, full, delta)
	return n, wrap("incr", key, err)
}

// TTL returns the time left before key expires, NoExpiry for a key that
// never expires, or ErrNotFound.
func (s *Store) TTL(ctx context.Context, key string) (time.Duration, error) {
	full, err := s.full("ttl", key)
	if err != nil {
		return 0, err
	}
	d, err := s.backend.TTL(ctx, full)
	return d, wrap("ttl", key, err)
}

// Expire makes key expire after ttl, or never when ttl is 0. It returns
// ErrNotFound when key is missing.
func (s *Store) Expire(ctx context.Context, key string, ttl time.Duration) error {
	full, err := s.full("expire", key)
	if err != nil {
		return err
	}
	if ttl < 0 {
		return &Error{Op: "expire", Key: key, Err: fmt.Errorf("%w: negative TTL", ErrInvalidOption)}
	}
	return wrap("expire", key, s.backend.Expire(ctx, full, ttl))
}

// Keys yields the keys in the store's namespace, without the namespace.
// WithPrefix limits them to keys that start with a prefix. The order is
// unspecified, and a key may repeat when the store changes during
// iteration. On failure Keys yields the error and stops.
func (s *Store) Keys(ctx context.Context, opts ...KeysOption) iter.Seq2[string, error] {
	return func(yield func(string, error) bool) {
		if err := s.check("keys"); err != nil {
			yield("", err)
			return
		}
		var o keysOptions
		for _, opt := range opts {
			opt(&o)
		}
		for key, err := range s.backend.Keys(ctx, s.prefix+o.prefix) {
			if err != nil {
				yield("", wrap("keys", "", err))
				return
			}
			if !yield(strings.TrimPrefix(key, s.prefix), nil) {
				return
			}
		}
	}
}

// Clear removes every key in the store's namespace. On the root store it
// returns ErrInvalidOption, so no call can empty a whole backend.
func (s *Store) Clear(ctx context.Context) error {
	if err := s.check("clear"); err != nil {
		return err
	}
	if s.prefix == "" {
		return &Error{Op: "clear", Err: fmt.Errorf("%w: Clear needs a namespace", ErrInvalidOption)}
	}
	return wrap("clear", "", s.backend.DeletePrefix(ctx, s.prefix))
}

// Ping verifies that the store is reachable.
func (s *Store) Ping(ctx context.Context) error {
	if err := s.check("ping"); err != nil {
		return err
	}
	return wrap("ping", "", s.backend.Ping(ctx))
}

// Close closes the Backend, which every namespace of the store shares.
// Later calls on any of them return ErrClosed. Close is safe to call more
// than once.
func (s *Store) Close() error {
	if s.closed.Swap(true) {
		return nil
	}
	return wrap("close", "", s.backend.Close())
}

// check returns the error that blocks every call on s, if any.
func (s *Store) check(op string) error {
	if s.err != nil {
		return s.err
	}
	if s.closed.Load() {
		return &Error{Op: op, Err: ErrClosed}
	}
	return nil
}

// full validates key and returns it with the namespace prefix.
func (s *Store) full(op, key string) (string, error) {
	if err := s.check(op); err != nil {
		return "", err
	}
	full := s.prefix + key
	switch {
	case key == "":
		return "", &Error{Op: op, Err: fmt.Errorf("%w: empty key", ErrInvalidKey)}
	case !utf8.ValidString(key):
		return "", &Error{Op: op, Err: fmt.Errorf("%w: key is not valid UTF-8", ErrInvalidKey)}
	case len(full) > MaxKeyLen:
		return "", &Error{Op: op, Err: fmt.Errorf("%w: key longer than %d bytes", ErrInvalidKey, MaxKeyLen)}
	}
	return full, nil
}

// wrap returns err as an *Error for op on key. It returns nil for nil and
// leaves an *Error unchanged.
func wrap(op, key string, err error) error {
	if err == nil {
		return nil
	}
	if e, ok := errors.AsType[*Error](err); ok {
		return e
	}
	return &Error{Op: op, Key: key, Err: err}
}
