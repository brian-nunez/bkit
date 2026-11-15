package stash

import "time"

// SetOption configures one Set call.
type SetOption func(*SetOptions)

// WithTTL makes the key expire after d. Zero means it never expires, the
// default. A negative d fails with ErrInvalidOption.
func WithTTL(d time.Duration) SetOption {
	return func(o *SetOptions) { o.TTL = d }
}

// IfNotExists stores the value only when the key is not set. Otherwise Set
// stores nothing and returns ErrExists. The check and the write are one
// atomic step, so exactly one of several racing callers succeeds.
func IfNotExists() SetOption {
	return func(o *SetOptions) { o.IfNotExists = true }
}

// KeysOption configures one Keys call.
type KeysOption func(*keysOptions)

// keysOptions holds the settings a Keys call collects.
type keysOptions struct {
	prefix string
}

// WithPrefix limits Keys to keys that start with prefix, matched literally.
func WithPrefix(prefix string) KeysOption {
	return func(o *keysOptions) { o.prefix = prefix }
}
