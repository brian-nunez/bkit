package settings_test

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"testing/fstest"
	"time"

	"github.com/brian-nunez/bkit/settings"
)

func mustLoad(t *testing.T, sources ...settings.Source) *settings.Config {
	t.Helper()
	cfg, err := settings.Load(context.Background(), sources...)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return cfg
}

func TestLoadMergesLeftToRight(t *testing.T) {
	fsys := fstest.MapFS{
		"base.json":     {Data: []byte(`{"name": {"first": "Ada", "last": "Lovelace"}, "port": 80, "tags": ["a", "b"]}`)},
		"override.json": {Data: []byte(`{"Name": {"First": "Grace"}, "port": null, "tags": ["c"]}`)},
	}
	cfg := mustLoad(t,
		settings.FileFS(fsys, "base.json", settings.JSON),
		settings.FileFS(fsys, "override.json", settings.JSON),
	)
	want := map[string]any{
		"name": map[string]any{"first": "Grace", "last": "Lovelace"},
		"port": nil,
		"tags": []any{"c"},
	}
	if got := cfg.Data(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Data() = %#v, want %#v", got, want)
	}
}

func TestLoadReplacesAcrossTypes(t *testing.T) {
	cfg := mustLoad(t,
		settings.Map(map[string]any{"a": map[string]any{"b": 1}, "c": "scalar"}),
		settings.Map(map[string]any{"a": "scalar", "c": map[string]any{"d": 2}}),
	)
	want := map[string]any{"a": "scalar", "c": map[string]any{"d": int64(2)}}
	if got := cfg.Data(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Data() = %#v, want %#v", got, want)
	}
}

func TestLoadNoSources(t *testing.T) {
	cfg := mustLoad(t)
	if got := cfg.Data(); len(got) != 0 {
		t.Fatalf("Data() = %#v, want empty", got)
	}
}

func TestLoadCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := settings.Load(ctx, settings.Map(nil))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	var se *settings.Error
	if !errors.As(err, &se) || se.Source != "map" {
		t.Fatalf("err = %#v, want *Error for source map", err)
	}
}

type ctxSource struct{ seen context.Context }

func (*ctxSource) Name() string { return "ctx" }

func (s *ctxSource) Load(ctx context.Context) (map[string]any, error) {
	s.seen = ctx
	return nil, nil
}

func TestLoadPassesContext(t *testing.T) {
	type key struct{}
	ctx := context.WithValue(context.Background(), key{}, "v")
	src := &ctxSource{}
	if _, err := settings.Load(ctx, src); err != nil {
		t.Fatal(err)
	}
	if src.seen.Value(key{}) != "v" {
		t.Fatal("source did not receive the caller's context")
	}
}

func TestLoadSourceError(t *testing.T) {
	_, err := settings.Load(context.Background(), settings.File(filepath.Join(t.TempDir(), "missing.json"), settings.JSON))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err = %v, want fs.ErrNotExist", err)
	}
}

func TestNormalize(t *testing.T) {
	type level uint8
	when := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	cfg := mustLoad(t, settings.Map(map[string]any{
		"int": 1, "int8": int8(2), "int16": int16(3), "int32": int32(4), "int64": int64(5),
		"uint": uint(6), "uint8": uint8(7), "uint16": uint16(8), "uint32": uint32(9), "uint64": uint64(10),
		"biguint": uint(math.MaxUint64), "biguint64": uint64(math.MaxUint64),
		"float32": float32(1.5), "float64": 2.5, "bool": true, "string": "s", "nil": nil,
		"jsonint": json.Number("11"), "jsonfloat": json.Number("1.25"), "jsonhuge": json.Number("1e400"),
		"intmap": map[int]string{1: "one"}, "array": [2]int{1, 2}, "strings": []string{"x"},
		"time": when, "level": level(3),
		"Mixed": map[string]any{"A": 1}, "mixed": map[string]any{"b": 2},
	}))
	want := map[string]any{
		"int": int64(1), "int8": int64(2), "int16": int64(3), "int32": int64(4), "int64": int64(5),
		"uint": int64(6), "uint8": int64(7), "uint16": int64(8), "uint32": int64(9), "uint64": int64(10),
		"biguint": uint64(math.MaxUint64), "biguint64": uint64(math.MaxUint64),
		"float32": 1.5, "float64": 2.5, "bool": true, "string": "s", "nil": nil,
		"jsonint": int64(11), "jsonfloat": 1.25, "jsonhuge": "1e400",
		"intmap": map[string]any{"1": "one"}, "array": []any{int64(1), int64(2)}, "strings": []any{"x"},
		"time": when, "level": level(3),
		"mixed": map[string]any{"a": int64(1), "b": int64(2)},
	}
	if got := cfg.Data(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Data() =\n%#v\nwant\n%#v", got, want)
	}
}

func TestCaseCollisionLaterSpellingWins(t *testing.T) {
	cfg := mustLoad(t, settings.Map(map[string]any{"name": "lower", "NAME": "upper"}))
	if got := cfg.GetString("name", ""); got != "lower" {
		t.Fatalf("name = %q, want lower (sorted after NAME)", got)
	}
}

func TestLoadCopiesSourceValues(t *testing.T) {
	inner := map[string]any{"b": "before"}
	list := []any{"x"}
	cfg := mustLoad(t, settings.Map(map[string]any{"a": inner, "list": list}))
	inner["b"] = "after"
	list[0] = "y"
	if got := cfg.GetString("a.b", ""); got != "before" {
		t.Fatalf("a.b = %q after source mutation", got)
	}
	data := cfg.Data()
	data["a"].(map[string]any)["b"] = "changed"
	data["list"].([]any)[0] = "changed"
	if got := cfg.GetString("a.b", ""); got != "before" {
		t.Fatalf("a.b = %q after Data mutation", got)
	}
	if got, _ := settings.Get[[]string](cfg, "list"); got[0] != "x" {
		t.Fatalf("list = %v after Data mutation", got)
	}
}

func TestConfigGet(t *testing.T) {
	cfg := mustLoad(t, settings.Map(map[string]any{
		"name": map[string]any{"first": "Ada"},
		"gone": nil,
	}))

	if v, ok := cfg.Get("Name.First"); !ok || v != "Ada" {
		t.Fatalf("Get(Name.First) = %v, %v", v, ok)
	}
	if v, ok := cfg.Get("gone"); !ok || v != nil {
		t.Fatalf("Get(gone) = %v, %v; want nil, true", v, ok)
	}
	for _, path := range []string{"missing", "name.missing", "name.first.deeper"} {
		if v, ok := cfg.Get(path); ok || v != nil {
			t.Fatalf("Get(%q) = %v, %v; want nil, false", path, v, ok)
		}
	}
	root, ok := cfg.Get("")
	if !ok || !reflect.DeepEqual(root, cfg.Data()) {
		t.Fatalf("Get(\"\") = %v, %v", root, ok)
	}
	sub, _ := cfg.Get("name")
	sub.(map[string]any)["first"] = "changed"
	if got := cfg.GetString("name.first", ""); got != "Ada" {
		t.Fatalf("Get returned shared map; name.first = %q", got)
	}
}

func TestZeroConfig(t *testing.T) {
	var cfg settings.Config
	if got := cfg.Data(); got == nil || len(got) != 0 {
		t.Fatalf("Data() = %#v, want empty non-nil map", got)
	}
	if got := cfg.GetString("a", "def"); got != "def" {
		t.Fatalf("GetString = %q", got)
	}
	var v struct{ A string }
	if err := cfg.Decode(&v); err != nil {
		t.Fatal(err)
	}
}

func TestErrorString(t *testing.T) {
	tests := []struct {
		err  *settings.Error
		want string
	}{
		{&settings.Error{Source: "env", Err: context.Canceled}, "settings: source env: context canceled"},
		{&settings.Error{Path: "a.b", Err: settings.ErrNotFound}, "settings: key a.b: not found"},
		{&settings.Error{Source: "s", Path: "p", Err: settings.ErrType}, "settings: source s: key p: type mismatch"},
	}
	for _, tt := range tests {
		if got := tt.err.Error(); got != tt.want {
			t.Errorf("Error() = %q, want %q", got, tt.want)
		}
		if !errors.Is(tt.err, tt.err.Err) {
			t.Errorf("errors.Is(%v, %v) = false", tt.err, tt.err.Err)
		}
	}
}

func TestFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"port": 8080}`), 0o600); err != nil {
		t.Fatal(err)
	}
	src := settings.File(path, settings.JSON)
	if got, want := src.Name(), "file:"+path; got != want {
		t.Fatalf("Name() = %q, want %q", got, want)
	}
	cfg := mustLoad(t, src)
	if got := cfg.GetInt("port", 0); got != 8080 {
		t.Fatalf("port = %d", got)
	}
}

func TestFileCanceledBeforeParse(t *testing.T) {
	fsys := fstest.MapFS{"c.json": {Data: []byte(`{}`)}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := settings.FileFS(fsys, "c.json", settings.JSON).Load(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestJSON(t *testing.T) {
	tests := []struct {
		name    string
		doc     string
		want    map[string]any
		wantErr bool
	}{
		{name: "object", doc: `{"a": 1, "a": 2}`, want: map[string]any{"a": int64(2)}},
		{name: "null", doc: `null`, want: map[string]any{}},
		{name: "empty", doc: ``, wantErr: true},
		{name: "array", doc: `[1]`, wantErr: true},
		{name: "invalid", doc: `{`, wantErr: true},
		{name: "trailing value", doc: `{} {}`, wantErr: true},
		{name: "trailing garbage", doc: `{} x`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fsys := fstest.MapFS{"c.json": {Data: []byte(tt.doc)}}
			cfg, err := settings.Load(context.Background(), settings.FileFS(fsys, "c.json", settings.JSON))
			if tt.wantErr {
				if !errors.Is(err, settings.ErrParse) {
					t.Fatalf("err = %v, want ErrParse", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := cfg.Data(); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Data() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestEnv(t *testing.T) {
	t.Setenv("BKITTEST_NAME__FIRST", "Ada")
	t.Setenv("BKITTEST_NAME__LAST", "Lovelace")
	t.Setenv("BKITTEST_SERVER_PORT", "8080")
	t.Setenv("BKITTEST_", "empty key once stripped")
	t.Setenv("OTHERTEST_FLAG", "true")

	t.Run("all variables", func(t *testing.T) {
		src := settings.Env()
		if src.Name() != "env" {
			t.Fatalf("Name() = %q", src.Name())
		}
		cfg := mustLoad(t, src)
		if got := cfg.GetString("bkittest_name.first", ""); got != "Ada" {
			t.Fatalf("bkittest_name.first = %q", got)
		}
		if got := cfg.GetInt("BKITTEST_SERVER_PORT", 0); got != 8080 {
			t.Fatalf("bkittest_server_port = %d", got)
		}
		if got := cfg.GetBool("othertest_flag", false); !got {
			t.Fatal("othertest_flag = false")
		}
	})

	t.Run("prefix removed", func(t *testing.T) {
		src := settings.Env(settings.WithPrefix("bkittest_"))
		if src.Name() != "env:bkittest_" {
			t.Fatalf("Name() = %q", src.Name())
		}
		got, _ := settings.Get[map[string]any](mustLoad(t, src), "")
		want := map[string]any{
			"name":        map[string]any{"first": "Ada", "last": "Lovelace"},
			"server_port": "8080",
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("tree = %#v, want %#v", got, want)
		}
	})

	t.Run("prefix kept", func(t *testing.T) {
		src := settings.Env(settings.WithPrefixPreserve("BKITTEST_"), settings.WithPrefix("OTHERTEST_"))
		if src.Name() != "env:BKITTEST_,OTHERTEST_" {
			t.Fatalf("Name() = %q", src.Name())
		}
		got := mustLoad(t, src).Data()
		want := map[string]any{
			"bkittest_name":        map[string]any{"first": "Ada", "last": "Lovelace"},
			"bkittest_server_port": "8080",
			"bkittest_":            "empty key once stripped",
			"flag":                 "true",
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("tree = %#v, want %#v", got, want)
		}
	})
}

func TestEnvFlatAndNestedSameName(t *testing.T) {
	t.Setenv("BKITTEST_DB", "flat")
	t.Setenv("BKITTEST_DB__HOST", "nested")
	cfg := mustLoad(t, settings.Env(settings.WithPrefix("BKITTEST_")))
	if got := cfg.GetString("db.host", ""); got != "nested" {
		t.Fatalf("db.host = %q, want nested (applied after DB)", got)
	}
}

func TestEnvOverridesFile(t *testing.T) {
	t.Setenv("APPTEST_SERVER__ADDR", ":9090")
	fsys := fstest.MapFS{"c.json": {Data: []byte(`{"server": {"addr": ":8080", "timeout": "5s"}}`)}}
	cfg := mustLoad(t,
		settings.FileFS(fsys, "c.json", settings.JSON),
		settings.Env(settings.WithPrefix("APPTEST_")),
	)
	if got := cfg.GetString("server.addr", ""); got != ":9090" {
		t.Fatalf("server.addr = %q", got)
	}
	if got := cfg.GetDuration("server.timeout", 0); got != 5*time.Second {
		t.Fatalf("server.timeout = %v", got)
	}
}

func TestJSONBigIntegers(t *testing.T) {
	doc := `{"over": 9223372036854775809, "max": 18446744073709551615,
		"beyond": 18446744073709551616, "negative": -9223372036854775809, "float": 1.5e3}`
	fsys := fstest.MapFS{"c.json": {Data: []byte(doc)}}
	cfg := mustLoad(t, settings.FileFS(fsys, "c.json", settings.JSON))

	want := map[string]any{
		"over": uint64(9223372036854775809), "max": uint64(math.MaxUint64),
		"beyond": "18446744073709551616", "negative": "-9223372036854775809", "float": 1500.0,
	}
	if got := cfg.Data(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Data() = %#v, want %#v", got, want)
	}
	if u, err := settings.Get[uint64](cfg, "over"); err != nil || u != 9223372036854775809 {
		t.Fatalf("Get[uint64](over) = %d, %v", u, err)
	}
	if u, err := settings.Get[uint64](cfg, "max"); err != nil || u != math.MaxUint64 {
		t.Fatalf("Get[uint64](max) = %d, %v", u, err)
	}
	if s, err := settings.Get[string](cfg, "beyond"); err != nil || s != "18446744073709551616" {
		t.Fatalf("Get[string](beyond) = %q, %v", s, err)
	}
	if f, err := settings.Get[float64](cfg, "beyond"); err != nil || f != 18446744073709551616 {
		t.Fatalf("Get[float64](beyond) = %v, %v", f, err)
	}
}
