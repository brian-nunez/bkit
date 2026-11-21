package settings

import (
	"fmt"
	"reflect"
	"time"
)

// Get converts the value at path to T. Paths are lowercased before lookup,
// and an empty path names the whole tree, so Get can also decode the root
// or any nested map into a struct. A missing key reports ErrNotFound. A
// value that cannot convert reports ErrType, including a null value when T
// cannot hold nil; for pointer, slice, map, and interface types a null value
// returns nil. See Config.Decode for the conversion rules.
func Get[T any](c *Config, path string) (T, error) {
	var out T
	key, v, ok := c.lookup(path)
	if !ok {
		return out, &Error{Path: key, Err: ErrNotFound}
	}
	rv := reflect.ValueOf(&out).Elem()
	if v == nil && !nilable(rv.Kind()) {
		return out, &Error{Path: key, Err: fmt.Errorf("%w: null cannot be %s", ErrType, rv.Type())}
	}
	if err := assign(rv, v, key); err != nil {
		var zero T
		return zero, err
	}
	return out, nil
}

func nilable(k reflect.Kind) bool {
	switch k {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Interface:
		return true
	default:
		return false
	}
}

// getOr returns Get's value, or def when Get fails.
func getOr[T any](c *Config, path string, def T) T {
	v, err := Get[T](c, path)
	if err != nil {
		return def
	}
	return v
}

// GetString returns the value at path as a string, or def when the key is
// missing, null, or not convertible. Numbers and booleans format as strings.
func (c *Config) GetString(path string, def string) string {
	return getOr(c, path, def)
}

// GetInt returns the value at path as an int, or def when the key is
// missing, null, or not convertible.
func (c *Config) GetInt(path string, def int) int {
	return getOr(c, path, def)
}

// GetBool returns the value at path as a bool, or def when the key is
// missing, null, or not convertible. Strings parse with strconv.ParseBool.
func (c *Config) GetBool(path string, def bool) bool {
	return getOr(c, path, def)
}

// GetFloat returns the value at path as a float64, or def when the key is
// missing, null, or not convertible.
func (c *Config) GetFloat(path string, def float64) float64 {
	return getOr(c, path, def)
}

// GetDuration returns the value at path as a time.Duration, or def when the
// key is missing, null, or not convertible. Strings parse with
// time.ParseDuration; integers are nanoseconds.
func (c *Config) GetDuration(path string, def time.Duration) time.Duration {
	return getOr(c, path, def)
}
