package settings

import (
	"encoding"
	"fmt"
	"maps"
	"math"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"
)

var durationType = reflect.TypeFor[time.Duration]()

// Decode fills the struct, map, or other value target points to from the
// whole configuration. target must be a non-nil pointer.
//
// Struct fields match keys by their `settings:"name"` tag, or by field name,
// without regard to case; a tag of "-" skips the field, and an untagged
// embedded struct's fields are matched as if they were the outer struct's.
// Fields with no matching key keep their current values, so a pre-filled
// struct supplies defaults. A null value sets a field to its zero value.
//
// Values convert as follows:
//   - a value whose type is assignable to the target is copied;
//   - a string fills a type implementing encoding.TextUnmarshaler;
//   - a string fills a time.Duration through time.ParseDuration, and an
//     integer fills it as nanoseconds;
//   - strings parse into booleans and base-10 numbers, and numbers and
//     booleans format into strings;
//   - numbers convert between numeric types when the value fits exactly;
//   - a list fills a slice, replacing it;
//   - a map fills a struct, or a map with string keys, keeping entries the
//     configuration does not set;
//   - a pointer is allocated when nil and filled through.
//
// Any other combination fails with ErrType and the path of the value.
func (c *Config) Decode(target any) error {
	rv := reflect.ValueOf(target)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return &Error{Err: fmt.Errorf("%w: Decode needs a non-nil pointer, got %T", ErrType, target)}
	}
	return assign(rv.Elem(), c.data, "")
}

// assign stores src, a normalized value, in the addressable dst.
func assign(dst reflect.Value, src any, path string) error {
	if src == nil {
		dst.SetZero()
		return nil
	}
	sv := reflect.ValueOf(src)
	if sv.Type().AssignableTo(dst.Type()) {
		dst.Set(reflect.ValueOf(clone(src)))
		return nil
	}
	if s, ok := src.(string); ok {
		if u, ok := dst.Addr().Interface().(encoding.TextUnmarshaler); ok {
			if err := u.UnmarshalText([]byte(s)); err != nil {
				return &Error{Path: path, Err: &textError{msg: typeMessage(src, dst.Type()), cause: err}}
			}
			return nil
		}
		if dst.Type() == durationType {
			d, err := time.ParseDuration(s)
			if err != nil {
				return typeError(path, src, dst.Type())
			}
			dst.SetInt(int64(d))
			return nil
		}
	}

	switch dst.Kind() {
	case reflect.Pointer:
		if dst.IsNil() {
			dst.Set(reflect.New(dst.Type().Elem()))
		}
		return assign(dst.Elem(), src, path)
	case reflect.Bool:
		b, ok := toBool(sv)
		if !ok {
			return typeError(path, src, dst.Type())
		}
		dst.SetBool(b)
	case reflect.String:
		s, ok := toString(sv)
		if !ok {
			return typeError(path, src, dst.Type())
		}
		dst.SetString(s)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		i, ok := toInt(sv)
		if !ok || dst.OverflowInt(i) {
			return typeError(path, src, dst.Type())
		}
		dst.SetInt(i)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		u, ok := toUint(sv)
		if !ok || dst.OverflowUint(u) {
			return typeError(path, src, dst.Type())
		}
		dst.SetUint(u)
	case reflect.Float32, reflect.Float64:
		f, ok := toFloat(sv)
		if !ok || dst.OverflowFloat(f) {
			return typeError(path, src, dst.Type())
		}
		dst.SetFloat(f)
	case reflect.Slice:
		return assignSlice(dst, src, path)
	case reflect.Map:
		return assignMap(dst, src, path)
	case reflect.Struct:
		m, ok := src.(map[string]any)
		if !ok {
			return typeError(path, src, dst.Type())
		}
		return assignStruct(dst, m, path)
	default:
		return typeError(path, src, dst.Type())
	}
	return nil
}

func assignSlice(dst reflect.Value, src any, path string) error {
	items, ok := src.([]any)
	if !ok {
		return typeError(path, src, dst.Type())
	}
	out := reflect.MakeSlice(dst.Type(), len(items), len(items))
	for i, item := range items {
		if err := assign(out.Index(i), item, path+"["+strconv.Itoa(i)+"]"); err != nil {
			return err
		}
	}
	dst.Set(out)
	return nil
}

func assignMap(dst reflect.Value, src any, path string) error {
	m, ok := src.(map[string]any)
	if !ok || dst.Type().Key().Kind() != reflect.String {
		return typeError(path, src, dst.Type())
	}
	if dst.IsNil() {
		dst.Set(reflect.MakeMapWithSize(dst.Type(), len(m)))
	}
	keyType, elemType := dst.Type().Key(), dst.Type().Elem()
	for _, k := range slices.Sorted(maps.Keys(m)) {
		key := reflect.ValueOf(k).Convert(keyType)
		elem := reflect.New(elemType).Elem()
		if existing := dst.MapIndex(key); existing.IsValid() {
			elem.Set(existing)
		}
		if err := assign(elem, m[k], join(path, k)); err != nil {
			return err
		}
		dst.SetMapIndex(key, elem)
	}
	return nil
}

func assignStruct(dst reflect.Value, m map[string]any, path string) error {
	t := dst.Type()
	for i := range t.NumField() {
		field := t.Field(i)
		tag := field.Tag.Get("settings")
		if tag == "-" {
			continue
		}
		if field.Anonymous && tag == "" && field.Type.Kind() == reflect.Struct {
			if err := assignStruct(dst.Field(i), m, path); err != nil {
				return err
			}
			continue
		}
		if !field.IsExported() {
			continue
		}
		name := tag
		if name == "" {
			name = field.Name
		}
		name = strings.ToLower(name)
		v, ok := m[name]
		if !ok {
			continue
		}
		if err := assign(dst.Field(i), v, join(path, name)); err != nil {
			return err
		}
	}
	return nil
}

func join(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

// typeError reports that src cannot convert to t. The message names src's
// type and never its value, which may be a secret.
func typeError(path string, src any, t reflect.Type) error {
	return &Error{Path: path, Err: fmt.Errorf("%w: %s", ErrType, typeMessage(src, t))}
}

func typeMessage(src any, t reflect.Type) string {
	return fmt.Sprintf("cannot use %T value as %s", src, t)
}

// textError reports a failed encoding.TextUnmarshaler. Its message leaves
// out the cause's text, which may quote the value, and it matches both
// ErrType and the cause.
type textError struct {
	msg   string
	cause error
}

func (e *textError) Error() string {
	return ErrType.Error() + ": " + e.msg
}

func (e *textError) Unwrap() []error {
	return []error{ErrType, e.cause}
}

func toBool(v reflect.Value) (bool, bool) {
	switch v.Kind() {
	case reflect.Bool:
		return v.Bool(), true
	case reflect.String:
		b, err := strconv.ParseBool(v.String())
		return b, err == nil
	}
	return false, false
}

func toString(v reflect.Value) (string, bool) {
	switch v.Kind() {
	case reflect.String:
		return v.String(), true
	case reflect.Bool:
		return strconv.FormatBool(v.Bool()), true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(v.Int(), 10), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return strconv.FormatUint(v.Uint(), 10), true
	case reflect.Float32, reflect.Float64:
		return strconv.FormatFloat(v.Float(), 'g', -1, 64), true
	}
	return "", false
}

func toInt(v reflect.Value) (int64, bool) {
	switch v.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int(), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		u := v.Uint()
		return int64(u), u <= math.MaxInt64
	case reflect.Float32, reflect.Float64:
		f := v.Float()
		return int64(f), f == math.Trunc(f) && f >= math.MinInt64 && f < math.MaxInt64
	case reflect.String:
		i, err := strconv.ParseInt(v.String(), 10, 64)
		return i, err == nil
	}
	return 0, false
}

func toUint(v reflect.Value) (uint64, bool) {
	switch v.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		i := v.Int()
		return uint64(i), i >= 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return v.Uint(), true
	case reflect.Float32, reflect.Float64:
		f := v.Float()
		return uint64(f), f == math.Trunc(f) && f >= 0 && f < math.MaxUint64
	case reflect.String:
		u, err := strconv.ParseUint(v.String(), 10, 64)
		return u, err == nil
	}
	return 0, false
}

func toFloat(v reflect.Value) (float64, bool) {
	switch v.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return float64(v.Int()), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return float64(v.Uint()), true
	case reflect.Float32, reflect.Float64:
		return v.Float(), true
	case reflect.String:
		f, err := strconv.ParseFloat(v.String(), 64)
		return f, err == nil
	}
	return 0, false
}
