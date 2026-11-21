package settings

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strconv"
	"strings"
)

// normalize converts a value from a source into the canonical tree: maps
// become map[string]any with lowercase keys, slices and arrays become []any,
// built-in integers become int64 (or uint64 beyond int64), float32 becomes
// float64, and json.Number becomes int64, uint64, or float64, keeping its
// text when it is an integer beyond uint64 or a number beyond float64.
// Other values are kept.
// The result shares no maps or slices with the input.
func normalize(v any) any {
	switch t := v.(type) {
	case nil, string, bool, int64, float64:
		return t
	case int:
		return int64(t)
	case int8:
		return int64(t)
	case int16:
		return int64(t)
	case int32:
		return int64(t)
	case uint:
		return normalizeUint(uint64(t))
	case uint8:
		return int64(t)
	case uint16:
		return int64(t)
	case uint32:
		return int64(t)
	case uint64:
		return normalizeUint(t)
	case float32:
		return float64(t)
	case json.Number:
		return normalizeNumber(t)
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Map:
		return normalizeMap(rv)
	case reflect.Slice, reflect.Array:
		out := make([]any, rv.Len())
		for i := range out {
			out[i] = normalize(rv.Index(i).Interface())
		}
		return out
	default:
		return v
	}
}

// normalizeNumber keeps integers exact: an integer becomes int64, or uint64
// beyond int64, and an integer outside both ranges keeps its decimal text.
// Other numbers become float64, or keep their text when out of range.
func normalizeNumber(n json.Number) any {
	s := n.String()
	if i, err := strconv.ParseInt(s, 10, 64); err == nil {
		return i
	}
	if u, err := strconv.ParseUint(s, 10, 64); err == nil {
		return u
	}
	if digits := strings.TrimLeft(s, "+-"); digits != "" && strings.Trim(digits, "0123456789") == "" {
		return s
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return f
	}
	return s
}

func normalizeUint(u uint64) any {
	if u > math.MaxInt64 {
		return u
	}
	return int64(u)
}

// normalizeMap normalizes a map of any key type. Keys that are equal once
// lowercased merge in sorted order of their original spelling.
func normalizeMap(rv reflect.Value) map[string]any {
	type entry struct {
		key   string
		value any
	}
	entries := make([]entry, 0, rv.Len())
	for iter := rv.MapRange(); iter.Next(); {
		entries = append(entries, entry{fmt.Sprint(iter.Key().Interface()), iter.Value().Interface()})
	}
	slices.SortFunc(entries, func(a, b entry) int { return strings.Compare(a.key, b.key) })

	out := make(map[string]any, len(entries))
	for _, e := range entries {
		key := strings.ToLower(e.key)
		out[key] = merge(out[key], normalize(e.value))
	}
	return out
}

// merge returns the result of applying src over dst: two maps merge key by
// key, and any other src replaces dst. It may modify dst.
func merge(dst, src any) any {
	if dm, ok := dst.(map[string]any); ok {
		if sm, ok := src.(map[string]any); ok {
			mergeMaps(dm, sm)
			return dm
		}
	}
	return src
}

func mergeMaps(dst, src map[string]any) {
	for k, v := range src {
		dst[k] = merge(dst[k], v)
	}
}

// clone deep-copies the maps and slices of a normalized value.
func clone(v any) any {
	switch t := v.(type) {
	case map[string]any:
		return cloneMap(t)
	case []any:
		out := make([]any, len(t))
		for i, item := range t {
			out[i] = clone(item)
		}
		return out
	}
	return v
}

func cloneMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = clone(v)
	}
	return out
}
