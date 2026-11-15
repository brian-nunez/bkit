package stash

import (
	"errors"
	"strings"
)

// Sentinel errors. They arrive wrapped in *Error; match them with errors.Is.
var (
	// ErrNotFound reports that the key does not exist or has expired.
	ErrNotFound = errors.New("stash: not found")

	// ErrExists reports that a Set with IfNotExists found the key already
	// set.
	ErrExists = errors.New("stash: key exists")

	// ErrNotInteger reports an Incr on a value that is not a base-10
	// integer, or an increment that would overflow int64.
	ErrNotInteger = errors.New("stash: value is not an integer")

	// ErrInvalidKey reports a key or namespace that is empty, is not valid
	// UTF-8, or makes the full key longer than MaxKeyLen bytes.
	ErrInvalidKey = errors.New("stash: invalid key")

	// ErrInvalidOption reports an option value that is out of range, or a
	// call that needs a namespace made on the root store.
	ErrInvalidOption = errors.New("stash: invalid option")

	// ErrCodec reports a value a Codec could not encode or decode.
	ErrCodec = errors.New("stash: codec failed")

	// ErrClosed reports a call on a closed store.
	ErrClosed = errors.New("stash: closed")

	// ErrUnknownDriver reports a URL whose scheme matches no driver passed
	// to Open.
	ErrUnknownDriver = errors.New("stash: unknown driver")

	// ErrInvalidURL reports a store URL that cannot be parsed or has no
	// scheme.
	ErrInvalidURL = errors.New("stash: invalid url")
)

// Error describes a failed operation. It matches Err, and any sentinel Err
// wraps, with errors.Is and errors.As. Its message never contains the store
// URL, a key, or a value, because keys often hold session IDs or tokens.
type Error struct {
	// Op names the operation, such as "get", "set", or "open".
	Op string

	// Key is the key involved, without the namespace, if any. It is left
	// out of the message; log it only when keys are not sensitive.
	Key string

	// Err is the cause: a sentinel, a context error, or a driver's error.
	Err error
}

// Error formats the operation and cause.
func (e *Error) Error() string {
	return "stash: " + e.Op + ": " + strings.TrimPrefix(e.Err.Error(), "stash: ")
}

// Unwrap returns Err.
func (e *Error) Unwrap() error {
	return e.Err
}
