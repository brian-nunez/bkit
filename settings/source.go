package settings

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"slices"
	"strings"
)

// Map returns a Source that supplies values from code, such as defaults.
// Load copies the values, so later changes to values do not affect a loaded
// Config. The source's name is "map".
func Map(values map[string]any) Source {
	return mapSource(values)
}

type mapSource map[string]any

func (mapSource) Name() string { return "map" }

func (m mapSource) Load(context.Context) (map[string]any, error) { return m, nil }

// File returns a Source that reads the file at path and parses it with
// format. The source's name is "file:" followed by path. A missing file
// fails with an error that matches fs.ErrNotExist; a parse failure matches
// ErrParse.
func File(path string, format Format) Source {
	return &fileSource{path: path, format: format}
}

// FileFS is File for a file in fsys, such as an embed.FS. The path follows
// the fs.FS rules.
func FileFS(fsys fs.FS, path string, format Format) Source {
	return &fileSource{fsys: fsys, path: path, format: format}
}

type fileSource struct {
	fsys   fs.FS
	path   string
	format Format
}

func (f *fileSource) Name() string { return "file:" + f.path }

func (f *fileSource) Load(ctx context.Context) (map[string]any, error) {
	var data []byte
	var err error
	if f.fsys == nil {
		data, err = os.ReadFile(f.path)
	} else {
		data, err = fs.ReadFile(f.fsys, f.path)
	}
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	values, err := f.format.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrParse, err)
	}
	return values, nil
}

// JSON is the JSON Format. The document must be one object; duplicate keys
// keep the last value, as in encoding/json. Integers keep full precision: an
// integer that fits neither int64 nor uint64 is kept as its decimal string.
var JSON Format = jsonFormat{}

type jsonFormat struct{}

func (jsonFormat) Parse(data []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var values map[string]any
	if err := dec.Decode(&values); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("unexpected data after the top-level JSON value")
	}
	return values, nil
}

// EnvOption configures Env.
type EnvOption func(*envSource)

// WithPrefix limits Env to variables whose names start with one of prefixes,
// compared without regard to case, and removes the matched prefix from the
// key: with WithPrefix("APP_"), APP_NAME__FIRST becomes name.first.
func WithPrefix(prefixes ...string) EnvOption {
	return withPrefixes(prefixes, true)
}

// WithPrefixPreserve limits Env like WithPrefix but keeps the prefix in the
// key: with WithPrefixPreserve("APP_"), APP_NAME__FIRST becomes
// app_name.first.
func WithPrefixPreserve(prefixes ...string) EnvOption {
	return withPrefixes(prefixes, false)
}

func withPrefixes(prefixes []string, strip bool) EnvOption {
	return func(e *envSource) {
		for _, p := range prefixes {
			e.prefixes = append(e.prefixes, envPrefix{value: p, strip: strip})
		}
	}
}

// Env returns a Source that reads environment variables when it loads. With
// no options it reads every variable. Each name is lowercased and split on
// "__" into a path, so NAME__FIRST becomes name.first and NAME becomes name.
// Values stay strings; getters convert them. When prefix options are given,
// a variable matching none is skipped, and the first matching prefix in
// option order decides whether the prefix is removed. A variable whose key
// is empty after its prefix is removed is skipped.
//
// The source's name is "env", or "env:" followed by the prefixes.
func Env(opts ...EnvOption) Source {
	e := &envSource{}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

type envPrefix struct {
	value string
	strip bool
}

type envSource struct {
	prefixes []envPrefix
}

func (e *envSource) Name() string {
	if len(e.prefixes) == 0 {
		return "env"
	}
	names := make([]string, len(e.prefixes))
	for i, p := range e.prefixes {
		names[i] = p.value
	}
	return "env:" + strings.Join(names, ",")
}

func (e *envSource) Load(context.Context) (map[string]any, error) {
	vars := os.Environ()
	slices.Sort(vars)
	out := make(map[string]any)
	for _, kv := range vars {
		name, value, _ := strings.Cut(kv, "=")
		key, ok := e.key(name)
		if !ok || key == "" {
			continue
		}
		parts := strings.Split(strings.ToLower(key), "__")
		var v any = value
		for i := len(parts) - 1; i > 0; i-- {
			v = map[string]any{parts[i]: v}
		}
		out[parts[0]] = merge(out[parts[0]], v)
	}
	return out, nil
}

// key applies the prefix options to a variable name.
func (e *envSource) key(name string) (string, bool) {
	if len(e.prefixes) == 0 {
		return name, true
	}
	for _, p := range e.prefixes {
		if len(name) >= len(p.value) && strings.EqualFold(name[:len(p.value)], p.value) {
			if p.strip {
				return name[len(p.value):], true
			}
			return name, true
		}
	}
	return "", false
}
