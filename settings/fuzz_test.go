package settings_test

import (
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"math/rand/v2"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/brian-nunez/bkit/settings"
)

// The fuzz targets and property tests below check the documented parsing,
// merging, and conversion rules against small reference models that are
// written independently of the implementation.

// refNormalize models the documented key and number rules: keys lowercase,
// keys equal once lowercased merge in sorted order of their original
// spelling, and numbers follow refNumber.
func refNormalize(v any) any {
	switch t := v.(type) {
	case map[string]any:
		groups := make(map[string][]string)
		for k := range t {
			lk := strings.ToLower(k)
			groups[lk] = append(groups[lk], k)
		}
		out := make(map[string]any, len(groups))
		for lk, keys := range groups {
			slices.Sort(keys)
			var acc any
			for _, k := range keys {
				acc = refMerge(acc, refNormalize(t[k]))
			}
			out[lk] = acc
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, item := range t {
			out[i] = refNormalize(item)
		}
		return out
	case json.Number:
		return refNumber(t.String())
	case uint64:
		if t <= math.MaxInt64 {
			return int64(t)
		}
		return t
	}
	return v
}

// refNumber models normalizeNumber with math/big: an integer literal is
// int64, else uint64, else its decimal text; any other number is float64,
// or its text when it does not fit.
func refNumber(s string) any {
	if !strings.ContainsAny(s, ".eE") {
		if b, ok := new(big.Int).SetString(s, 10); ok {
			switch {
			case b.IsInt64():
				return b.Int64()
			case b.IsUint64():
				return b.Uint64()
			default:
				return s
			}
		}
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return f
	}
	return s
}

// refMerge models merging without modifying either argument: two maps merge
// key by key, and any other later value replaces the earlier one.
func refMerge(earlier, later any) any {
	em, ok := earlier.(map[string]any)
	if !ok {
		return later
	}
	lm, ok := later.(map[string]any)
	if !ok {
		return later
	}
	out := make(map[string]any, len(em)+len(lm))
	for k, v := range em {
		out[k] = v
	}
	for k, v := range lm {
		out[k] = refMerge(out[k], v)
	}
	return out
}

// deepCopy copies the maps and slices of a source tree.
func deepCopy(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, item := range t {
			out[k] = deepCopy(item)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, item := range t {
			out[i] = deepCopy(item)
		}
		return out
	}
	return v
}

// checkCanonical fails unless every key in v is lowercase and every value
// has a canonical tree type.
func checkCanonical(t *testing.T, v any, path string) {
	t.Helper()
	switch x := v.(type) {
	case nil, string, bool, int64, float64:
	case uint64:
		if x <= math.MaxInt64 {
			t.Fatalf("%q: uint64 %d fits int64", path, x)
		}
	case []any:
		for i, item := range x {
			checkCanonical(t, item, path+"["+strconv.Itoa(i)+"]")
		}
	case map[string]any:
		for k, item := range x {
			if strings.ToLower(k) != k {
				t.Fatalf("%q: key %q is not lowercase", path, k)
			}
			checkCanonical(t, item, path+"."+k)
		}
	default:
		t.Fatalf("%q: value of non-canonical type %T", path, v)
	}
}

// checkLaterWins fails unless every non-map value of later appears unchanged
// at its path in merged, and every key of every map in later exists there,
// including keys set to null.
func checkLaterWins(t *testing.T, merged, later any, path string) {
	t.Helper()
	lm, ok := later.(map[string]any)
	if !ok {
		if !reflect.DeepEqual(merged, later) {
			t.Fatalf("%q: merged %#v, later source set %#v", path, merged, later)
		}
		return
	}
	mm, ok := merged.(map[string]any)
	if !ok {
		t.Fatalf("%q: merged %#v, want a map", path, merged)
	}
	for k, v := range lm {
		mv, ok := mm[k]
		if !ok {
			t.Fatalf("%q: key %q from the later source is missing", path, k)
		}
		checkLaterWins(t, mv, v, path+"."+k)
	}
}

// checkCaseFold fails unless each top-level key group that differs only in
// case takes a non-map value from the last key in sorted order.
func checkCaseFold(t *testing.T, raw, got map[string]any) {
	t.Helper()
	last := make(map[string]string)
	for k := range raw {
		lk := strings.ToLower(k)
		if cur, ok := last[lk]; !ok || k > cur {
			last[lk] = k
		}
	}
	for lk, k := range last {
		want := refNormalize(raw[k])
		if _, isMap := want.(map[string]any); isMap {
			continue
		}
		if !reflect.DeepEqual(got[lk], want) {
			t.Fatalf("key %q = %#v, want %#v from %q", lk, got[lk], want, k)
		}
	}
}

// checkMergeLaws checks Load with Map sources against the merge model and
// the merge laws.
func checkMergeLaws(t *testing.T, a, b, c map[string]any) {
	t.Helper()
	before := deepCopy(a)
	empty := map[string]any{}

	wantA := refNormalize(a)
	gotA := mustLoad(t, settings.Map(a)).Data()
	if !reflect.DeepEqual(gotA, wantA) {
		t.Fatalf("Load(a) = %#v, want %#v", gotA, wantA)
	}
	checkCanonical(t, gotA, "")
	checkCaseFold(t, a, gotA)

	if got := mustLoad(t, settings.Map(a), settings.Map(a)).Data(); !reflect.DeepEqual(got, wantA) {
		t.Fatalf("Load(a, a) = %#v, want %#v", got, wantA)
	}
	if got := mustLoad(t, settings.Map(a), settings.Map(empty)).Data(); !reflect.DeepEqual(got, wantA) {
		t.Fatalf("Load(a, {}) = %#v, want %#v", got, wantA)
	}
	if got := mustLoad(t, settings.Map(empty), settings.Map(a)).Data(); !reflect.DeepEqual(got, wantA) {
		t.Fatalf("Load({}, a) = %#v, want %#v", got, wantA)
	}
	if got := mustLoad(t, settings.Map(gotA)).Data(); !reflect.DeepEqual(got, wantA) {
		t.Fatalf("Load(Load(a)) = %#v, want %#v", got, wantA)
	}

	wantB := refNormalize(b)
	wantAB := refMerge(wantA, wantB)
	gotAB := mustLoad(t, settings.Map(a), settings.Map(b)).Data()
	if !reflect.DeepEqual(gotAB, wantAB) {
		t.Fatalf("Load(a, b) = %#v, want %#v", gotAB, wantAB)
	}
	checkLaterWins(t, gotAB, wantB, "")

	wantABC := refMerge(wantAB, refNormalize(c))
	gotABC := mustLoad(t, settings.Map(a), settings.Map(b), settings.Map(c)).Data()
	if !reflect.DeepEqual(gotABC, wantABC) {
		t.Fatalf("Load(a, b, c) = %#v, want %#v", gotABC, wantABC)
	}
	if got := mustLoad(t, settings.Map(gotAB), settings.Map(c)).Data(); !reflect.DeepEqual(got, gotABC) {
		t.Fatalf("Load(Load(a, b), c) = %#v, want Load(a, b, c) = %#v", got, gotABC)
	}

	if !reflect.DeepEqual(a, before) {
		t.Fatalf("Load modified its source: %#v, was %#v", a, before)
	}
}

// jsonSpace trims the JSON whitespace characters.
func jsonSpace(data []byte) string {
	return strings.Trim(string(data), " \t\r\n")
}

func FuzzJSONParse(f *testing.F) {
	for _, seed := range []string{
		`{}`,
		` { "A" : 1 } `,
		`{"Key":{"Nested":{"DEEP":true}}}`,
		`{"a":1,"A":2}`,
		`{"Ab":{"x":1},"aB":2,"ab":{"y":1}}`,
		`{"a":{"x":1},"A":{"y":2}}`,
		`{"K":1,"k":2}`,
		`{"n":9223372036854775807,"m":-9223372036854775808}`,
		`{"n":9223372036854775808,"m":18446744073709551615}`,
		`{"n":18446744073709551616,"m":-9223372036854775809}`,
		`{"n":1.5e400,"m":1e-400,"z":-0,"f":0.5}`,
		`{"a":null}`,
		`{"list":[{"A":1},[2,{"B":3}],"s",true,null]}`,
		`{"a":1,"a":2}`,
		`{"a":1} {}`,
		`{"a":1}x`,
		`[]`,
		`1`,
		`"x"`,
		`{`,
		``,
		"{\"\xff\":1}",
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		values, err := settings.JSON.Parse(data)
		trimmed := jsonSpace(data)
		if err != nil {
			if json.Valid(data) && strings.HasPrefix(trimmed, "{") {
				t.Fatalf("Parse(%q) rejected a valid object: %v", data, err)
			}
			return
		}
		if !json.Valid(data) {
			t.Fatalf("Parse(%q) accepted invalid JSON", data)
		}
		switch {
		case strings.HasPrefix(trimmed, "{"):
		default:
			t.Fatalf("Parse(%q) accepted a non-object: %#v", data, values)
		}

		got := mustLoad(t, settings.Map(values)).Data()
		checkCanonical(t, got, "")
		if want := refNormalize(values); !reflect.DeepEqual(got, want) {
			t.Fatalf("Load(Parse(%q)) = %#v, want %#v", data, got, want)
		}
	})
}

func FuzzJSONNumber(f *testing.F) {
	for _, seed := range []string{
		"0", "-0", "7", "-7",
		"9223372036854775807", "-9223372036854775808",
		"9223372036854775808", "-9223372036854775809",
		"18446744073709551615", "18446744073709551616",
		"123456789012345678901234567890",
		"1.5", "-0.25", "1e2", "1E2", "1.0", "0.1e1",
		"9.223372036854775807e18", "1e400", "-1e400", "1e-400",
		"01", "+1", "abc", "1,\"x\":2",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, s string) {
		dec := json.NewDecoder(strings.NewReader(s))
		dec.UseNumber()
		var decoded any
		if err := dec.Decode(&decoded); err != nil || !json.Valid([]byte(s)) {
			return
		}
		n, ok := decoded.(json.Number)
		if !ok {
			return
		}
		num := n.String()
		data := []byte(`{"N":` + s + `}`)
		values, err := settings.JSON.Parse(data)
		if err != nil {
			t.Fatalf("Parse(%q) failed: %v", data, err)
		}
		cfg := mustLoad(t, settings.Map(values))
		got, ok := cfg.Get("n")
		if !ok {
			t.Fatalf("Get(n) missing for %q", num)
		}
		checkNumber(t, num, got)
		checkTyped(t, got)
	})
}

// checkNumber checks one normalized JSON number against the rules stated in
// terms of math/big.
func checkNumber(t *testing.T, num string, got any) {
	t.Helper()
	b, isInt := new(big.Int).SetString(num, 10)
	isInt = isInt && !strings.ContainsAny(num, ".eE")
	switch {
	case isInt && b.IsInt64():
		if v, ok := got.(int64); !ok || v != b.Int64() {
			t.Fatalf("%s normalized to %#v, want int64 %s", num, got, b)
		}
	case isInt && b.IsUint64():
		if v, ok := got.(uint64); !ok || v != b.Uint64() {
			t.Fatalf("%s normalized to %#v, want uint64 %s", num, got, b)
		}
		if _, err := settings.Get[int64](mustLoad(t, settings.Map(map[string]any{"n": got})), "n"); !errors.Is(err, settings.ErrType) {
			t.Fatalf("Get[int64] of %s = %v, want ErrType", num, err)
		}
	case isInt:
		if v, ok := got.(string); !ok || v != num {
			t.Fatalf("%s normalized to %#v, want its decimal text", num, got)
		}
	default:
		f, err := strconv.ParseFloat(num, 64)
		if err != nil {
			if v, ok := got.(string); !ok || v != num {
				t.Fatalf("%s normalized to %#v, want its text", num, got)
			}
			return
		}
		if v, ok := got.(float64); !ok || v != f {
			t.Fatalf("%s normalized to %#v, want float64 %v", num, got, f)
		}
	}
}

func FuzzMerge(f *testing.F) {
	for _, seed := range [][3]string{
		{`{"a":1}`, `{"a":2}`, `{}`},
		{`{"a":{"b":1,"c":2}}`, `{"A":{"B":3}}`, `{"a":null}`},
		{`{"a":{"b":1}}`, `{"a":5}`, `{"a":{"c":1}}`},
		{`{"a":5}`, `{"a":{"b":1}}`, `{"A":{"c":[1,2]}}`},
		{`{"Ab":{"x":1},"aB":2,"ab":{"y":1}}`, `{}`, `{"AB":null}`},
		{`{"x":[1,{"Y":2}]}`, `{"x":[3]}`, `{"X":"s"}`},
		{`{"n":null}`, `{"N":{"m":1}}`, `{"n":{"M":null}}`},
		{`{"k":1,"K":2}`, `{"K":{"a":1}}`, `{}`},
	} {
		f.Add([]byte(seed[0]), []byte(seed[1]), []byte(seed[2]))
	}
	f.Fuzz(func(t *testing.T, a, b, c []byte) {
		var trees [3]map[string]any
		for i, data := range [][]byte{a, b, c} {
			values, err := settings.JSON.Parse(data)
			if err != nil {
				return
			}
			trees[i] = values
		}
		checkMergeLaws(t, trees[0], trees[1], trees[2])
	})
}

const envPrefix = "BKITFZ_"

// envSettable reports whether the environment accepts the variable.
func envSettable(name, value string) bool {
	return name != "" && !strings.ContainsAny(name, "=\x00") && !strings.Contains(value, "\x00")
}

// envPath is the documented key path of a variable's key, or nil when the
// key is empty and the variable is skipped.
func envPath(key string) []string {
	if key == "" {
		return nil
	}
	return strings.Split(strings.ToLower(key), "__")
}

// nest builds the documented tree below the first element of path.
func nest(path []string, value string) any {
	var v any = value
	for i := len(path) - 1; i > 0; i-- {
		v = map[string]any{path[i]: v}
	}
	return v
}

// at returns the value at path in tree.
func at(tree map[string]any, path []string) (any, bool) {
	var cur any = tree
	for _, part := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = m[part]; !ok {
			return nil, false
		}
	}
	return cur, true
}

// isPrefix reports whether p is a prefix of q.
func isPrefix(p, q []string) bool {
	return len(p) <= len(q) && slices.Equal(p, q[:len(p)])
}

// checkEnvTree checks the tree an Env source loaded for two variables whose
// keys have the paths p1 and p2.
func checkEnvTree(t *testing.T, got map[string]any, p1, p2 []string, v1, v2 string, onlyThese bool) {
	t.Helper()
	checkCanonical(t, got, "")
	if onlyThese {
		for k := range got {
			if (p1 == nil || k != p1[0]) && (p2 == nil || k != p2[0]) {
				t.Fatalf("unexpected key %q in %#v", k, got)
			}
		}
		if p1 == nil && p2 == nil && len(got) != 0 {
			t.Fatalf("Env loaded %#v, want nothing", got)
		}
	}
	for _, c := range []struct {
		path, other []string
		value       string
	}{{p1, p2, v1}, {p2, p1, v2}} {
		if c.path == nil {
			continue
		}
		if c.other == nil || c.path[0] != c.other[0] {
			if want := nest(c.path, c.value); !reflect.DeepEqual(got[c.path[0]], want) {
				t.Fatalf("key %q = %#v, want %#v", c.path[0], got[c.path[0]], want)
			}
		}
		if c.other == nil || (!isPrefix(c.path, c.other) && !isPrefix(c.other, c.path)) {
			if v, ok := at(got, c.path); !ok || v != c.value {
				t.Fatalf("path %q = %#v, %v, want %q", c.path, v, ok, c.value)
			}
		}
	}
}

// checkEnv sets two prefixed variables and checks what Env loads with
// WithPrefix or WithPrefixPreserve, and with no options. The first
// variable's prefix is lowercase when lower is set, to check that prefixes
// match without regard to case.
func checkEnv(t *testing.T, name1, value1, name2, value2 string, lower, preserve bool) {
	t.Helper()
	prefix := envPrefix
	if lower {
		prefix = strings.ToLower(prefix)
	}
	full1, full2 := prefix+name1, envPrefix+name2
	if !envSettable(full1, value1) || !envSettable(full2, value2) || full1 == full2 {
		return
	}
	t.Setenv(full1, value1)
	t.Setenv(full2, value2)

	opt, key1, key2 := settings.WithPrefix(envPrefix), name1, name2
	if preserve {
		opt, key1, key2 = settings.WithPrefixPreserve(envPrefix), full1, full2
	}
	got := mustLoad(t, settings.Env(opt)).Data()
	checkEnvTree(t, got, envPath(key1), envPath(key2), value1, value2, true)

	all := mustLoad(t, settings.Env()).Data()
	checkEnvTree(t, all, envPath(full1), envPath(full2), value1, value2, false)
}

func FuzzEnv(f *testing.F) {
	f.Add("NAME__FIRST", "v", "OTHER", "w", false, false)
	f.Add("NAME__FIRST", "v", "NAME__LAST", "w", true, true)
	f.Add("__LEAD", "a=b=c", "TRAIL__", "", true, false)
	f.Add("A____B", "x", "a__c", "y", false, true)
	f.Add("", "skipped", "É__ß", "ü", false, false)
	f.Add("A", "1", "A__B", "2", false, false)
	f.Add("__", "=", "___", "==", false, false)
	f.Add("K", "upper", "k", "lower", true, false)
	f.Add("x.y", "dot", "\xff\xfe", "bytes", false, true)
	f.Fuzz(checkEnv)
}

// wantInt64 is the documented conversion of a normalized value to int64.
func wantInt64(v any) (int64, bool) {
	switch x := v.(type) {
	case int64:
		return x, true
	case uint64:
		b := new(big.Int).SetUint64(x)
		return b.Int64(), b.IsInt64()
	case float64:
		b, ok := floatInt(x)
		return b.Int64(), ok && b.IsInt64()
	case string:
		i, err := strconv.ParseInt(x, 10, 64)
		return i, err == nil
	}
	return 0, false
}

// wantUint64 is the documented conversion of a normalized value to uint64.
func wantUint64(v any) (uint64, bool) {
	switch x := v.(type) {
	case int64:
		return uint64(x), x >= 0
	case uint64:
		return x, true
	case float64:
		b, ok := floatInt(x)
		return b.Uint64(), ok && b.IsUint64()
	case string:
		u, err := strconv.ParseUint(x, 10, 64)
		return u, err == nil
	}
	return 0, false
}

// floatInt returns f as an integer when f has no fractional part.
func floatInt(f float64) (*big.Int, bool) {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return new(big.Int), false
	}
	bf := big.NewFloat(f)
	b, _ := bf.Int(nil)
	return b, bf.IsInt()
}

func wantInt(v any) (int, bool) {
	i, ok := wantInt64(v)
	return int(i), ok && int64(int(i)) == i
}

// wantFloat64 is the conversion of a normalized value to float64. Integers
// convert only when float64 holds them exactly.
func wantFloat64(v any) (float64, bool) {
	switch x := v.(type) {
	case int64:
		f, acc := new(big.Float).SetInt64(x).Float64()
		return f, acc == big.Exact
	case uint64:
		f, acc := new(big.Float).SetUint64(x).Float64()
		return f, acc == big.Exact
	case float64:
		return x, true
	case string:
		if strings.Trim(x, "0123456789+-.eE") != "" {
			return 0, false
		}
		f, err := strconv.ParseFloat(x, 64)
		return f, err == nil
	}
	return 0, false
}

func wantBool(v any) (bool, bool) {
	switch x := v.(type) {
	case bool:
		return x, true
	case string:
		b, err := strconv.ParseBool(x)
		return b, err == nil
	}
	return false, false
}

func wantString(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case bool:
		return strconv.FormatBool(x), true
	case int64:
		return strconv.FormatInt(x, 10), true
	case uint64:
		return strconv.FormatUint(x, 10), true
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64), true
	}
	return "", false
}

func wantDuration(v any) (time.Duration, bool) {
	if s, ok := v.(string); ok {
		d, err := time.ParseDuration(s)
		return d, err == nil
	}
	i, ok := wantInt64(v)
	return time.Duration(i), ok
}

// sameValue compares two converted values, treating NaN as equal to NaN.
func sameValue(a, b any) bool {
	if fa, ok := a.(float64); ok {
		if fb, ok := b.(float64); ok && math.IsNaN(fa) && math.IsNaN(fb) {
			return true
		}
	}
	return a == b
}

// checkConv checks Get[T] and Decode for the value at "v" in cfg against
// the expected conversion.
func checkConv[T any](t *testing.T, cfg *settings.Config, want T, ok bool) {
	t.Helper()
	for _, path := range []string{"v", "V"} {
		got, err := settings.Get[T](cfg, path)
		if !ok {
			var se *settings.Error
			if !errors.Is(err, settings.ErrType) || !errors.As(err, &se) || se.Path != "v" {
				t.Fatalf("Get[%T](%q) = %v, %v, want ErrType at v", got, path, got, err)
			}
			if !reflect.ValueOf(&got).Elem().IsZero() {
				t.Fatalf("Get[%T](%q) failed with non-zero value %v", got, path, got)
			}
			continue
		}
		if err != nil || !sameValue(got, want) {
			t.Fatalf("Get[%T](%q) = %v, %v, want %v", got, path, got, err, want)
		}
	}

	var dst struct{ V T }
	err := cfg.Decode(&dst)
	switch {
	case ok && (err != nil || !sameValue(dst.V, want)):
		t.Fatalf("Decode into %T = %v, %v, want %v like Get", dst.V, dst.V, err, want)
	case !ok && !errors.Is(err, settings.ErrType):
		t.Fatalf("Decode into %T = %v, want ErrType like Get", dst.V, err)
	}
}

// checkTyped loads v under an uppercase key and checks every typed read of
// it.
func checkTyped(t *testing.T, v any) {
	t.Helper()
	cfg := mustLoad(t, settings.Map(map[string]any{"V": v}))
	stored, _ := cfg.Get("v")
	i, ok := wantInt(stored)
	checkConv(t, cfg, i, ok)
	i64, ok := wantInt64(stored)
	checkConv(t, cfg, i64, ok)
	u64, ok := wantUint64(stored)
	checkConv(t, cfg, u64, ok)
	f64, ok := wantFloat64(stored)
	checkConv(t, cfg, f64, ok)
	b, ok := wantBool(stored)
	checkConv(t, cfg, b, ok)
	s, ok := wantString(stored)
	checkConv(t, cfg, s, ok)
	d, ok := wantDuration(stored)
	checkConv(t, cfg, d, ok)
}

// checkPath reads path from a fixed tree and checks that the lowercased
// path decides whether the key exists.
func checkPath(t *testing.T, s string, i int64, u uint64, f float64, path string) {
	t.Helper()
	cfg := mustLoad(t, settings.Map(map[string]any{
		"S": s, "I": i, "U": u, "F": f, "N": nil, "M": map[string]any{"X": s},
	}))
	known := map[string]bool{"": true, "s": true, "i": true, "u": true, "f": true, "n": true, "m": true, "m.x": true}
	lp := strings.ToLower(path)

	got, err := settings.Get[any](cfg, path)
	if known[lp] {
		if err != nil {
			t.Fatalf("Get[any](%q) failed: %v", path, err)
		}
		if (lp == "s" || lp == "m.x") && got != s {
			t.Fatalf("Get[any](%q) = %#v, want %q", path, got, s)
		}
	} else {
		var se *settings.Error
		if !errors.Is(err, settings.ErrNotFound) || !errors.As(err, &se) || se.Path != lp {
			t.Fatalf("Get[any](%q) = %v, want ErrNotFound at %q", path, err, lp)
		}
	}
	// A null reads as ErrType through Get and sets the zero value through
	// Decode.
	if _, err := settings.Get[int](cfg, "N"); !errors.Is(err, settings.ErrType) {
		t.Fatalf("Get[int](N) of null = %v, want ErrType", err)
	}
	dst := struct{ N int }{N: 1}
	if err := mustLoad(t, settings.Map(map[string]any{"N": nil})).Decode(&dst); err != nil || dst.N != 0 {
		t.Fatalf("Decode of null = %d, %v, want 0, nil", dst.N, err)
	}
	if _, err := settings.Get[int](cfg, path); err != nil && !errors.Is(err, settings.ErrType) && !errors.Is(err, settings.ErrNotFound) {
		t.Fatalf("Get[int](%q) = %v, want ErrType or ErrNotFound", path, err)
	}
	if _, err := settings.Get[time.Duration](cfg, path); err != nil && !errors.Is(err, settings.ErrType) && !errors.Is(err, settings.ErrNotFound) {
		t.Fatalf("Get[time.Duration](%q) = %v, want ErrType or ErrNotFound", path, err)
	}
}

func FuzzGet(f *testing.F) {
	f.Add("42", int64(42), uint64(42), 42.0, "S")
	f.Add("-9223372036854775808", int64(math.MinInt64), uint64(math.MaxInt64), -9.223372036854775808e18, "m.X")
	f.Add("18446744073709551615", int64(math.MaxInt64), uint64(math.MaxUint64), 9.223372036854775807e18, "M.x.y")
	f.Add("1.5", int64(-1), uint64(1<<63), 1.5, "")
	f.Add("true", int64(1<<53+1), uint64(1<<53+1), 1.8446744073709552e19, "N")
	f.Add("1h30m", int64(0), uint64(0), math.Inf(1), "missing")
	f.Add("-1s", int64(-5), uint64(5), math.NaN(), "s.")
	f.Add("1e3", int64(7), uint64(7), math.Copysign(0, -1), ".")
	f.Add("NaN", int64(7), uint64(7), 1e-300, "İ")
	f.Fuzz(func(t *testing.T, s string, i int64, u uint64, fl float64, path string) {
		for _, v := range []any{s, i, u, fl, s != "", []any{s}, map[string]any{"S": s}} {
			checkTyped(t, v)
		}
		checkPath(t, s, i, u, fl, path)
	})
}

// Deterministic property tests over generated input.

func newRand() *rand.Rand {
	return rand.New(rand.NewPCG(1, 2))
}

// randKeys collide often once lowercased, including the Kelvin sign, which
// lowercases to k.
var randKeys = []string{"a", "A", "b", "B", "ab", "Ab", "aB", "AB", "k", "K", "K"}

func randScalar(r *rand.Rand) any {
	switch r.IntN(8) {
	case 0:
		return []int64{0, -1, 1, math.MaxInt64, math.MinInt64, 1<<53 + 1, r.Int64()}[r.IntN(7)]
	case 1:
		return []uint64{0, 7, math.MaxInt64, 1 << 63, math.MaxUint64, r.Uint64()}[r.IntN(6)]
	case 2:
		return []float64{0, 1.5, -0.25, 3, 1e300, -1e300, 1 << 63, -(1 << 63), 1 << 64, 0.1}[r.IntN(10)]
	case 3:
		return []string{"", "42", "-7", "1.5", "true", "1s", "x", "18446744073709551615", "9223372036854775808"}[r.IntN(9)]
	case 4:
		return r.IntN(2) == 0
	case 5:
		return nil
	case 6:
		return []any{randScalarLeaf(r), randScalarLeaf(r)}
	default:
		return []any{map[string]any{randKeys[r.IntN(len(randKeys))]: randScalarLeaf(r)}}
	}
}

func randScalarLeaf(r *rand.Rand) any {
	for {
		switch v := randScalar(r).(type) {
		case []any:
		default:
			return v
		}
	}
}

func randTree(r *rand.Rand, depth int) map[string]any {
	n := r.IntN(5)
	out := make(map[string]any, n)
	for range n {
		k := randKeys[r.IntN(len(randKeys))]
		if depth > 0 && r.IntN(3) == 0 {
			out[k] = randTree(r, depth-1)
		} else {
			out[k] = randScalar(r)
		}
	}
	return out
}

// randCase changes the case of each ASCII letter at random.
func randCase(r *rand.Rand, s string) string {
	b := []byte(s)
	for i, c := range b {
		if r.IntN(2) == 0 && c >= 'a' && c <= 'z' {
			b[i] = c - 'a' + 'A'
		}
	}
	return string(b)
}

// leafPaths lists the paths of the non-map values in tree.
func leafPaths(tree map[string]any, prefix []string) [][]string {
	var out [][]string
	for k, v := range tree {
		path := append(slices.Clone(prefix), k)
		if m, ok := v.(map[string]any); ok && len(m) > 0 {
			out = append(out, leafPaths(m, path)...)
			continue
		}
		out = append(out, path)
	}
	return out
}

func TestPropertyMergeLaws(t *testing.T) {
	r := newRand()
	for range 500 {
		checkMergeLaws(t, randTree(r, 3), randTree(r, 3), randTree(r, 3))
	}
}

func TestPropertyGetIgnoresPathCase(t *testing.T) {
	r := newRand()
	for range 300 {
		cfg := mustLoad(t, settings.Map(randTree(r, 3)), settings.Map(randTree(r, 3)))
		data := cfg.Data()
		for _, path := range leafPaths(data, nil) {
			want, _ := at(data, path)
			lower := strings.Join(path, ".")
			got, ok := cfg.Get(randCase(r, lower))
			if !ok || !reflect.DeepEqual(got, want) {
				t.Fatalf("Get(%q) = %#v, %v, want %#v", lower, got, ok, want)
			}
			if _, err := settings.Get[string](cfg, strings.ToUpper(lower)+".missing"); !errors.Is(err, settings.ErrNotFound) {
				t.Fatalf("Get below leaf %q = %v, want ErrNotFound", lower, err)
			}
		}
	}
}

func TestPropertyTypedReads(t *testing.T) {
	r := newRand()
	edges := []any{
		int64(math.MaxInt32) + 1, int64(math.MinInt32) - 1, uint64(math.MaxUint32) + 1,
		math.NaN(), math.Inf(1), math.Inf(-1), math.Copysign(0, -1),
		math.MaxFloat64, math.SmallestNonzeroFloat64, 0.5, -0.5, 0x1p63, 0x1p64, -0x1p63,
		math.Nextafter(0x1p63, 0), math.Nextafter(0x1p64, 0), math.Nextafter(-0x1p63, math.Inf(-1)),
		"", " 1", "+1", "0x10", "1_000", "1e3", "-0", "inf", "t", "FALSE", "300ms", "-1.5h", "1d",
	}
	for _, v := range edges {
		checkTyped(t, v)
	}
	for range 2000 {
		var v any
		switch r.IntN(5) {
		case 0:
			v = int64(r.Uint64())
		case 1:
			v = r.Uint64()
		case 2:
			v = math.Float64frombits(r.Uint64())
		case 3:
			v = float64(r.Int64N(1<<20)) / float64(int64(1)<<r.IntN(4))
		default:
			v = strconv.FormatInt(r.Int64(), 10)
		}
		checkTyped(t, v)
	}
}

func TestPropertyEnv(t *testing.T) {
	r := newRand()
	segments := []string{"A", "b", "_", "__", "É", "ß", "1", "Ω", "x.y", "K"}
	randName := func() string {
		var b strings.Builder
		for range r.IntN(5) {
			b.WriteString(segments[r.IntN(len(segments))])
		}
		return b.String()
	}
	values := []string{"", "v", "a=b", "=", "ü", "with space"}
	for i := range 200 {
		name1, name2 := randName(), randName()
		v1, v2 := values[r.IntN(len(values))], values[r.IntN(len(values))]
		lower, preserve := r.IntN(2) == 0, r.IntN(2) == 0
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			checkEnv(t, name1, v1, name2, v2, lower, preserve)
		})
	}
}
