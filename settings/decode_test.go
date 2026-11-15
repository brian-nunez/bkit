package settings_test

import (
	"errors"
	"fmt"
	"math"
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/brian-nunez/bkit/settings"
)

type mode string

type level uint8

type upper string

type flag bool

func (u *upper) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		return errors.New("empty")
	}
	*u = upper(strings.ToUpper(string(b)))
	return nil
}

func load(t *testing.T, m map[string]any) *settings.Config {
	t.Helper()
	return mustLoad(t, settings.Map(m))
}

func TestGetConversions(t *testing.T) {
	cfg := load(t, map[string]any{
		"str": "text", "int": 42, "neg": -1, "float": 2.5, "whole": 3.0, "bool": true,
		"sint": "-7", "suint": "7", "sfloat": "1.5", "sbool": "true", "sdur": "1m30s",
		"big": uint64(math.MaxUint64), "huge": 1e30, "level": level(3), "mode": mode("fast"),
		"addr": "127.0.0.1", "list": []any{"a", "b"}, "nil": nil,
		"map": map[string]any{"a": 1, "b": 2},
	})

	check := func(name string, got, want any, err error) {
		t.Helper()
		if err != nil {
			t.Errorf("%s: unexpected error %v", name, err)
			return
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s = %#v, want %#v", name, got, want)
		}
	}

	s, err := settings.Get[string](cfg, "int")
	check("string<-int", s, "42", err)
	s, err = settings.Get[string](cfg, "float")
	check("string<-float", s, "2.5", err)
	s, err = settings.Get[string](cfg, "bool")
	check("string<-bool", s, "true", err)
	s, err = settings.Get[string](cfg, "big")
	check("string<-uint64", s, "18446744073709551615", err)
	m, err := settings.Get[mode](cfg, "str")
	check("mode<-string", m, mode("text"), err)

	i, err := settings.Get[int](cfg, "sint")
	check("int<-string", i, -7, err)
	i, err = settings.Get[int](cfg, "whole")
	check("int<-whole float", i, 3, err)
	i, err = settings.Get[int](cfg, "level")
	check("int<-uint kind", i, 3, err)
	i8, err := settings.Get[int8](cfg, "int")
	check("int8<-int", i8, int8(42), err)

	u, err := settings.Get[uint](cfg, "suint")
	check("uint<-string", u, uint(7), err)
	u, err = settings.Get[uint](cfg, "int")
	check("uint<-int", u, uint(42), err)
	u, err = settings.Get[uint](cfg, "whole")
	check("uint<-float", u, uint(3), err)
	u64, err := settings.Get[uint64](cfg, "big")
	check("uint64<-uint64", u64, uint64(math.MaxUint64), err)

	f, err := settings.Get[float64](cfg, "sfloat")
	check("float<-string", f, 1.5, err)
	f, err = settings.Get[float64](cfg, "int")
	check("float<-int", f, 42.0, err)
	f, err = settings.Get[float64](cfg, "big")
	check("float<-uint64", f, float64(math.MaxUint64), err)
	f32, err := settings.Get[float32](cfg, "float")
	check("float32<-float", f32, float32(2.5), err)

	b, err := settings.Get[bool](cfg, "sbool")
	check("bool<-string", b, true, err)
	fl, err := settings.Get[flag](cfg, "bool")
	check("named bool<-bool", fl, flag(true), err)

	d, err := settings.Get[time.Duration](cfg, "sdur")
	check("duration<-string", d, 90*time.Second, err)
	d, err = settings.Get[time.Duration](cfg, "int")
	check("duration<-int", d, 42*time.Nanosecond, err)
	dp, err := settings.Get[*time.Duration](cfg, "sdur")
	if err != nil || dp == nil || *dp != 90*time.Second {
		t.Errorf("*duration = %v, %v", dp, err)
	}

	addr, err := settings.Get[netip.Addr](cfg, "addr")
	check("TextUnmarshaler", addr, netip.MustParseAddr("127.0.0.1"), err)
	up, err := settings.Get[upper](cfg, "str")
	check("custom TextUnmarshaler", up, upper("TEXT"), err)

	l, err := settings.Get[[]string](cfg, "list")
	check("[]string", l, []string{"a", "b"}, err)
	mm, err := settings.Get[map[string]int](cfg, "map")
	check("map[string]int", mm, map[string]int{"a": 1, "b": 2}, err)
	mk, err := settings.Get[map[mode]any](cfg, "map")
	check("map[named]any", mk, map[mode]any{"a": int64(1), "b": int64(2)}, err)
	a, err := settings.Get[any](cfg, "list")
	check("any", a, []any{"a", "b"}, err)

	p, err := settings.Get[*string](cfg, "nil")
	check("*string<-nil", p, (*string)(nil), err)
	sl, err := settings.Get[[]int](cfg, "nil")
	check("[]int<-nil", sl, []int(nil), err)
	a, err = settings.Get[any](cfg, "nil")
	check("any<-nil", a, nil, err)
}

func TestGetErrors(t *testing.T) {
	cfg := load(t, map[string]any{
		"str": "text", "int": 42, "neg": -1, "float": 2.5, "bool": true, "nil": nil,
		"big": uint64(math.MaxUint64), "huge": 1e30, "max": math.MaxFloat64, "list": []any{"a", 1.5},
		"map": map[string]any{"a": "x"}, "empty": "",
	})

	tests := []struct {
		name string
		get  func() error
		path string
		want error
	}{
		{"missing", func() error { _, err := settings.Get[string](cfg, "Missing"); return err }, "missing", settings.ErrNotFound},
		{"nil to string", func() error { _, err := settings.Get[string](cfg, "nil"); return err }, "nil", settings.ErrType},
		{"map to string", func() error { _, err := settings.Get[string](cfg, "map"); return err }, "map", settings.ErrType},
		{"string to bool", func() error { _, err := settings.Get[bool](cfg, "str"); return err }, "str", settings.ErrType},
		{"int to bool", func() error { _, err := settings.Get[bool](cfg, "int"); return err }, "int", settings.ErrType},
		{"string to int", func() error { _, err := settings.Get[int](cfg, "str"); return err }, "str", settings.ErrType},
		{"fraction to int", func() error { _, err := settings.Get[int](cfg, "float"); return err }, "float", settings.ErrType},
		{"huge to int", func() error { _, err := settings.Get[int64](cfg, "huge"); return err }, "huge", settings.ErrType},
		{"uint64 to int", func() error { _, err := settings.Get[int64](cfg, "big"); return err }, "big", settings.ErrType},
		{"overflow int8", func() error { _, err := settings.Get[int8](cfg, "big"); return err }, "big", settings.ErrType},
		{"int8 overflow", func() error { _, err := settings.Get[int8](cfg, "huge"); return err }, "huge", settings.ErrType},
		{"bool to int", func() error { _, err := settings.Get[int](cfg, "bool"); return err }, "bool", settings.ErrType},
		{"negative to uint", func() error { _, err := settings.Get[uint](cfg, "neg"); return err }, "neg", settings.ErrType},
		{"fraction to uint", func() error { _, err := settings.Get[uint](cfg, "float"); return err }, "float", settings.ErrType},
		{"string to uint", func() error { _, err := settings.Get[uint](cfg, "str"); return err }, "str", settings.ErrType},
		{"bool to uint", func() error { _, err := settings.Get[uint](cfg, "bool"); return err }, "bool", settings.ErrType},
		{"uint8 fits", func() error { _, err := settings.Get[uint8](cfg, "int"); return err }, "", nil},
		{"uint8 too big", func() error { _, err := settings.Get[uint8](cfg, "big"); return err }, "big", settings.ErrType},
		{"string to float", func() error { _, err := settings.Get[float64](cfg, "str"); return err }, "str", settings.ErrType},
		{"bool to float", func() error { _, err := settings.Get[float64](cfg, "bool"); return err }, "bool", settings.ErrType},
		{"float32 fits", func() error { _, err := settings.Get[float32](cfg, "huge"); return err }, "", nil},
		{"float32 overflow", func() error { _, err := settings.Get[float32](cfg, "max"); return err }, "max", settings.ErrType},
		{"bad duration", func() error { _, err := settings.Get[time.Duration](cfg, "str"); return err }, "str", settings.ErrType},
		{"bad text", func() error { _, err := settings.Get[upper](cfg, "empty"); return err }, "empty", settings.ErrType},
		{"scalar to slice", func() error { _, err := settings.Get[[]string](cfg, "str"); return err }, "str", settings.ErrType},
		{"mixed elements to strings", func() error { _, err := settings.Get[[]string](cfg, "list"); return err }, "", nil},
		{"bad element int", func() error { _, err := settings.Get[[]int](cfg, "list"); return err }, "list[0]", settings.ErrType},
		{"scalar to map", func() error { _, err := settings.Get[map[string]string](cfg, "str"); return err }, "str", settings.ErrType},
		{"int keys", func() error { _, err := settings.Get[map[int]string](cfg, "map"); return err }, "map", settings.ErrType},
		{"bad map value", func() error { _, err := settings.Get[map[string]int](cfg, "map"); return err }, "map.a", settings.ErrType},
		{"scalar to struct", func() error { _, err := settings.Get[struct{ A string }](cfg, "str"); return err }, "str", settings.ErrType},
		{"unsupported kind", func() error { _, err := settings.Get[chan int](cfg, "str"); return err }, "str", settings.ErrType},
		{"interface", func() error { _, err := settings.Get[fmt.Stringer](cfg, "str"); return err }, "str", settings.ErrType},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.get()
			if tt.want == nil {
				// Values that fit, listed to pin the boundary next to the failures.
				if err != nil {
					t.Fatalf("unexpected error %v", err)
				}
				return
			}
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
			var se *settings.Error
			if !errors.As(err, &se) || se.Path != tt.path {
				t.Fatalf("err = %#v, want path %q", err, tt.path)
			}
		})
	}
}

func TestGetReturnsZeroOnError(t *testing.T) {
	cfg := load(t, map[string]any{"s": map[string]any{"a": "1", "b": "x"}})
	got, err := settings.Get[map[string]int](cfg, "s")
	if err == nil || got != nil {
		t.Fatalf("Get = %v, %v; want nil map and error", got, err)
	}
}

func TestDefaultGetters(t *testing.T) {
	cfg := load(t, map[string]any{
		"s": "v", "i": "12", "b": "false", "f": "0.5", "d": "2s", "bad": map[string]any{},
	})
	if got := cfg.GetString("s", "def"); got != "v" {
		t.Errorf("GetString = %q", got)
	}
	if got := cfg.GetInt("i", 1); got != 12 {
		t.Errorf("GetInt = %d", got)
	}
	if got := cfg.GetBool("b", true); got {
		t.Errorf("GetBool = %v", got)
	}
	if got := cfg.GetFloat("f", 1); got != 0.5 {
		t.Errorf("GetFloat = %v", got)
	}
	if got := cfg.GetDuration("d", 0); got != 2*time.Second {
		t.Errorf("GetDuration = %v", got)
	}
	if got := cfg.GetString("bad", "def"); got != "def" {
		t.Errorf("GetString(bad) = %q", got)
	}
	if got := cfg.GetInt("missing", 7); got != 7 {
		t.Errorf("GetInt(missing) = %d", got)
	}
	if got := cfg.GetBool("missing", true); !got {
		t.Errorf("GetBool(missing) = %v", got)
	}
	if got := cfg.GetFloat("missing", 1.5); got != 1.5 {
		t.Errorf("GetFloat(missing) = %v", got)
	}
	if got := cfg.GetDuration("missing", time.Minute); got != time.Minute {
		t.Errorf("GetDuration(missing) = %v", got)
	}
}

type Embedded struct {
	Region string
}

type inner struct {
	Zone string
}

type appConfig struct {
	Embedded
	inner
	Name     string            `settings:"display_name"`
	Port     int               `settings:"Port"`
	Timeout  time.Duration     `settings:"timeout"`
	Debug    *bool             `settings:"debug"`
	Tags     []string          `settings:"tags"`
	Labels   map[string]string `settings:"labels"`
	Limits   map[string]limit  `settings:"limits"`
	Server   server            `settings:"server"`
	Skipped  string            `settings:"-"`
	Default  string            `settings:"default"`
	Nullable *server           `settings:"nullable"`
	private  string
}

type server struct {
	Host string
	Port int
}

type limit struct {
	Max int
	Min int
}

func TestDecode(t *testing.T) {
	cfg := load(t, map[string]any{
		"region":       "eu",
		"zone":         "b",
		"display_name": "svc",
		"port":         "8080",
		"timeout":      "1s",
		"debug":        "true",
		"tags":         []any{"a"},
		"labels":       map[string]any{"team": "core"},
		"limits":       map[string]any{"cpu": map[string]any{"max": 4}},
		"server":       map[string]any{"HOST": "localhost"},
		"skipped":      "ignored",
		"private":      "ignored",
		"nullable":     nil,
	})

	got := appConfig{
		Default:  "kept",
		Server:   server{Port: 9000},
		Labels:   map[string]string{"env": "dev"},
		Limits:   map[string]limit{"cpu": {Min: 1}},
		Nullable: &server{Host: "set"},
		private:  "kept",
	}
	if err := cfg.Decode(&got); err != nil {
		t.Fatal(err)
	}
	debug := true
	want := appConfig{
		Embedded: Embedded{Region: "eu"},
		inner:    inner{Zone: "b"},
		Name:     "svc",
		Port:     8080,
		Timeout:  time.Second,
		Debug:    &debug,
		Tags:     []string{"a"},
		Labels:   map[string]string{"env": "dev", "team": "core"},
		Limits:   map[string]limit{"cpu": {Max: 4, Min: 1}},
		Server:   server{Host: "localhost", Port: 9000},
		Default:  "kept",
		private:  "kept",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Decode =\n%+v\nwant\n%+v", got, want)
	}
}

func TestDecodeErrors(t *testing.T) {
	cfg := load(t, map[string]any{"server": map[string]any{"port": "x"}, "region": []any{}})

	if err := cfg.Decode(appConfig{}); !errors.Is(err, settings.ErrType) {
		t.Fatalf("non-pointer: err = %v", err)
	}
	if err := cfg.Decode((*appConfig)(nil)); !errors.Is(err, settings.ErrType) {
		t.Fatalf("nil pointer: err = %v", err)
	}

	var withField struct{ Server server }
	err := cfg.Decode(&withField)
	var se *settings.Error
	if !errors.As(err, &se) || se.Path != "server.port" || !errors.Is(err, settings.ErrType) {
		t.Fatalf("field: err = %v", err)
	}

	var withEmbedded struct{ Embedded }
	err = cfg.Decode(&withEmbedded)
	if !errors.As(err, &se) || se.Path != "region" {
		t.Fatalf("embedded: err = %v", err)
	}
}

// errEmpty is the cause secretText.UnmarshalText wraps.
var errEmpty = errors.New("empty")

// secretText rejects every value with an error that quotes it.
type secretText string

func (s *secretText) UnmarshalText(b []byte) error {
	return fmt.Errorf("bad value %q: %w", b, errEmpty)
}

func TestErrorsOmitValues(t *testing.T) {
	cfg := load(t, map[string]any{"db": map[string]any{"password": "hunter2", "host": "db.local"}})

	tests := []struct {
		name string
		err  func() error
		want string
	}{
		{"scalar", func() error { _, err := settings.Get[int](cfg, "db.password"); return err },
			"settings: key db.password: type mismatch: cannot use string value as int"},
		{"duration", func() error { _, err := settings.Get[time.Duration](cfg, "db.password"); return err },
			"settings: key db.password: type mismatch: cannot use string value as time.Duration"},
		{"section", func() error { var s struct{ DB string }; return cfg.Decode(&s) },
			"settings: key db: type mismatch: cannot use map[string]interface {} value as string"},
		{"text", func() error { _, err := settings.Get[secretText](cfg, "db.password"); return err },
			"settings: key db.password: type mismatch: cannot use string value as settings_test.secretText"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.err()
			if !errors.Is(err, settings.ErrType) {
				t.Fatalf("err = %v, want ErrType", err)
			}
			if got := err.Error(); got != tt.want {
				t.Fatalf("err = %q, want %q", got, tt.want)
			}
			if strings.Contains(err.Error(), "hunter2") || strings.Contains(err.Error(), "db.local") {
				t.Fatalf("error message contains a value: %q", err)
			}
		})
	}

	_, err := settings.Get[secretText](cfg, "db.password")
	if !errors.Is(err, errEmpty) {
		t.Fatalf("err = %v, want the TextUnmarshaler cause in the chain", err)
	}
}

func TestDecodeMergesAnyMaps(t *testing.T) {
	type attrs map[string]any
	cfg := load(t, map[string]any{
		"extra": map[string]any{"b": 2, "db": map[string]any{"port": 5433}, "list": []any{"z"}, "gone": nil},
		"named": map[string]any{"b": 2},
		"value": map[string]any{"b": 2},
		"items": []any{"z"},
	})

	type target struct {
		Extra map[string]any
		Named attrs
		Value any
		Items []any
	}
	got := target{
		Extra: map[string]any{
			"a": 1, "db": map[string]any{"host": "db.local", "port": 5432},
			"list": []any{"x", "y"}, "gone": "here",
		},
		Named: attrs{"a": 1},
		Value: map[string]any{"a": 1},
		Items: []any{"x", "y"},
	}
	if err := cfg.Decode(&got); err != nil {
		t.Fatal(err)
	}
	want := target{
		Extra: map[string]any{
			"a": 1, "b": int64(2), "db": map[string]any{"host": "db.local", "port": int64(5433)},
			"list": []any{"z"}, "gone": nil,
		},
		Named: attrs{"a": 1, "b": int64(2)},
		// A field of type any is replaced, like any value assignable to it.
		Value: map[string]any{"b": int64(2)},
		// A list replaces a slice.
		Items: []any{"z"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Decode =\n%#v\nwant\n%#v", got, want)
	}

	// The merged maps share nothing with the configuration.
	got.Extra["db"].(map[string]any)["port"] = 1
	got.Extra["list"].([]any)[0] = "changed"
	if v, _ := settings.Get[int](cfg, "extra.db.port"); v != 5433 {
		t.Fatalf("extra.db.port = %d after changing the decoded map", v)
	}
	if v, _ := settings.Get[[]string](cfg, "extra.list"); v[0] != "z" {
		t.Fatalf("extra.list = %v after changing the decoded slice", v)
	}

	if m, err := settings.Get[map[string]any](cfg, "extra"); err != nil || len(m) != 4 {
		t.Fatalf("Get[map[string]any] = %#v, %v", m, err)
	}
}
