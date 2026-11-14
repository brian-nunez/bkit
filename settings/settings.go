package settings

import (
	"context"
	"reflect"
	"strings"
)

// Source supplies part of a configuration.
type Source interface {
	// Name identifies the source in errors, such as "file:config.yaml".
	Name() string

	// Load returns the source's values as a tree of maps. It must honor ctx
	// and must not keep goroutines running after it returns. Load
	// normalizes the result, so keys may use any case and nested maps may
	// use any map type.
	Load(ctx context.Context) (map[string]any, error)
}

// Format parses a document into a tree of maps.
type Format interface {
	// Parse returns the document's values. Load normalizes the result the
	// same way it normalizes a Source's.
	Parse(data []byte) (map[string]any, error)
}

// Config is a merged, immutable configuration. Its zero value is an empty
// configuration.
type Config struct {
	data map[string]any
}

// Load reads every source in order and merges the results: later sources
// override earlier ones, and maps merge key by key. It stops at the first
// source that fails, or before the next source once ctx is done, and returns
// an *Error naming that source. Callers must not pass a nil Source.
func Load(ctx context.Context, sources ...Source) (*Config, error) {
	data := make(map[string]any)
	for _, src := range sources {
		if err := ctx.Err(); err != nil {
			return nil, &Error{Source: src.Name(), Err: err}
		}
		values, err := src.Load(ctx)
		if err != nil {
			return nil, &Error{Source: src.Name(), Err: err}
		}
		mergeMaps(data, normalizeMap(reflect.ValueOf(values)))
	}
	return &Config{data: data}, nil
}

// Get returns the value at path and reports whether the key exists. A key
// set to null returns nil and true. An empty path returns the whole tree.
// Maps and slices in the result are copies.
func (c *Config) Get(path string) (any, bool) {
	_, v, ok := c.lookup(path)
	return clone(v), ok
}

// Data returns a copy of the whole configuration tree.
func (c *Config) Data() map[string]any {
	return cloneMap(c.data)
}

// lookup returns the lowercased path and the value it names.
func (c *Config) lookup(path string) (string, any, bool) {
	key := strings.ToLower(path)
	var cur any = c.data
	if key == "" {
		return key, cur, true
	}
	for part := range strings.SplitSeq(key, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return key, nil, false
		}
		if cur, ok = m[part]; !ok {
			return key, nil, false
		}
	}
	return key, cur, true
}
