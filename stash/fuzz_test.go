package stash_test

import (
	"context"
	"errors"
	"iter"
	"maps"
	"math/rand/v2"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/brian-nunez/bkit/stash"
)

// sep joins a namespace to its keys, as Store.Namespace documents.
const sep = ":"

// spyBackend is a fakeBackend that records every key the Store passes to
// it, so tests can prove which full keys reach the backend.
type spyBackend struct {
	*fakeBackend

	mu   sync.Mutex
	keys []string
}

func newSpy() *spyBackend { return &spyBackend{fakeBackend: newFake()} }

func (s *spyBackend) record(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys = append(s.keys, key)
}

func (s *spyBackend) seen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.keys)
}

func (s *spyBackend) Get(ctx context.Context, key string) ([]byte, error) {
	s.record(key)
	return s.fakeBackend.Get(ctx, key)
}

func (s *spyBackend) Set(ctx context.Context, key string, value []byte, o stash.SetOptions) error {
	s.record(key)
	return s.fakeBackend.Set(ctx, key, value, o)
}

func (s *spyBackend) Delete(ctx context.Context, key string) (bool, error) {
	s.record(key)
	return s.fakeBackend.Delete(ctx, key)
}

func (s *spyBackend) Exists(ctx context.Context, key string) (bool, error) {
	s.record(key)
	return s.fakeBackend.Exists(ctx, key)
}

func (s *spyBackend) Incr(ctx context.Context, key string, delta int64) (int64, error) {
	s.record(key)
	return s.fakeBackend.Incr(ctx, key, delta)
}

func (s *spyBackend) TTL(ctx context.Context, key string) (time.Duration, error) {
	s.record(key)
	return s.fakeBackend.TTL(ctx, key)
}

func (s *spyBackend) Expire(ctx context.Context, key string, ttl time.Duration) error {
	s.record(key)
	return s.fakeBackend.Expire(ctx, key, ttl)
}

func (s *spyBackend) Keys(ctx context.Context, prefix string) iter.Seq2[string, error] {
	s.record(prefix)
	return s.fakeBackend.Keys(ctx, prefix)
}

func (s *spyBackend) DeletePrefix(ctx context.Context, prefix string) error {
	s.record(prefix)
	return s.fakeBackend.DeletePrefix(ctx, prefix)
}

// validKey reports whether key under prefix is a key Store accepts: non-empty,
// valid UTF-8, and at most MaxKeyLen bytes with its namespace.
func validKey(prefix, key string) bool {
	return key != "" && utf8.ValidString(key) && len(prefix)+len(key) <= stash.MaxKeyLen
}

// validName reports whether name is a namespace Store.Namespace accepts.
func validName(name string) bool {
	return name != "" && utf8.ValidString(name)
}

// FuzzKeyValidation checks every key-taking Store method against the key
// rules. A valid key reaches the backend as namespace prefix plus key and
// behaves like a fresh key. An invalid key or namespace fails every method
// with ErrInvalidKey and never reaches the backend.
func FuzzKeyValidation(f *testing.F) {
	long := strings.Repeat("k", stash.MaxKeyLen)
	accents := strings.Repeat("é", stash.MaxKeyLen/2) // MaxKeyLen bytes
	seeds := []struct {
		ns    string
		useNS bool
		key   string
	}{
		{"", false, "k"},
		{"", false, ""},
		{"", false, "\xff"},
		{"", false, "a\xc3"},
		{"", false, long},
		{"", false, long + "k"},
		{"", false, accents},
		{"", false, accents + "é"},
		{"ns", true, long[:stash.MaxKeyLen-len("ns"+sep)]},
		{"ns", true, long[:stash.MaxKeyLen-len("ns"+sep)+1]},
		{"ns", true, accents},
		{"ns", true, ""},
		{"ns", true, "a:b"},
		{"ns", true, ":"},
		{"ns", true, "ns:k"},
		{"a:b", true, "c"},
		{"", true, "k"},
		{"\xff", true, "k"},
		{long, true, "k"},
		{long[:stash.MaxKeyLen-2], true, "k"},
	}
	for _, s := range seeds {
		f.Add(s.ns, s.useNS, s.key, int64(1))
	}
	f.Add("ns", true, "counter", int64(-1)<<63)
	f.Fuzz(func(t *testing.T, ns string, useNS bool, key string, delta int64) {
		spy := newSpy()
		s := stash.New(spy)
		prefix, nsOK := "", true
		if useNS {
			s = s.Namespace(ns)
			prefix, nsOK = ns+sep, validName(ns)
		}
		if nsOK && validKey(prefix, key) {
			checkAccepted(t, s, spy, key, prefix+key, delta)
			return
		}
		checkRejected(t, s, spy, key, delta)
	})
}

// checkAccepted runs every key-taking method on a valid key and checks each
// result, and that the backend saw only full.
func checkAccepted(t *testing.T, s *stash.Store, spy *spyBackend, key, full string, delta int64) {
	t.Helper()
	ctx := t.Context()
	if _, err := s.Get(ctx, key); !errors.Is(err, stash.ErrNotFound) {
		t.Fatalf("Get(missing) err = %v, want ErrNotFound", err)
	}
	if ok, err := s.Exists(ctx, key); ok || err != nil {
		t.Fatalf("Exists(missing) = %v, %v, want false, nil", ok, err)
	}
	if _, err := s.TTL(ctx, key); !errors.Is(err, stash.ErrNotFound) {
		t.Fatalf("TTL(missing) err = %v, want ErrNotFound", err)
	}
	if err := s.Expire(ctx, key, time.Hour); !errors.Is(err, stash.ErrNotFound) {
		t.Fatalf("Expire(missing) err = %v, want ErrNotFound", err)
	}
	if ok, err := s.Delete(ctx, key); ok || err != nil {
		t.Fatalf("Delete(missing) = %v, %v, want false, nil", ok, err)
	}
	if n, err := s.Incr(ctx, key, delta); n != delta || err != nil {
		t.Fatalf("Incr(missing, %d) = %d, %v, want %d, nil", delta, n, err, delta)
	}
	if v, err := s.Get(ctx, key); string(v) != strconv.FormatInt(delta, 10) || err != nil {
		t.Fatalf("Get(counter) = %q, %v, want %d", v, err, delta)
	}
	if err := s.Set(ctx, key, []byte(key)); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if v, err := s.Get(ctx, key); string(v) != key || err != nil {
		t.Fatalf("Get = %q, %v, want the stored value", v, err)
	}
	if _, err := s.Incr(ctx, key, 1); !errors.Is(err, stash.ErrNotInteger) && !isInteger(key) {
		t.Fatalf("Incr(text) err = %v, want ErrNotInteger", err)
	}
	if err := s.Set(ctx, key, []byte("v"), stash.IfNotExists()); !errors.Is(err, stash.ErrExists) {
		t.Fatalf("Set(IfNotExists) err = %v, want ErrExists", err)
	}
	if ok, err := s.Exists(ctx, key); !ok || err != nil {
		t.Fatalf("Exists = %v, %v, want true, nil", ok, err)
	}
	if d, err := s.TTL(ctx, key); d != stash.NoExpiry || err != nil {
		t.Fatalf("TTL = %v, %v, want NoExpiry", d, err)
	}
	if err := s.Expire(ctx, key, time.Hour); err != nil {
		t.Fatalf("Expire: %v", err)
	}
	if d, err := s.TTL(ctx, key); d <= 0 || d > time.Hour || err != nil {
		t.Fatalf("TTL after Expire = %v, %v, want (0, 1h]", d, err)
	}
	if ok, err := s.Delete(ctx, key); !ok || err != nil {
		t.Fatalf("Delete = %v, %v, want true, nil", ok, err)
	}
	for _, got := range spy.seen() {
		if got != full {
			t.Fatalf("backend saw key %q, want %q", got, full)
		}
	}
}

// isInteger reports whether v parses as a base-10 int64, which Incr accepts.
func isInteger(v string) bool {
	_, err := strconv.ParseInt(v, 10, 64)
	return err == nil
}

// checkRejected runs every key-taking method on an invalid key or namespace
// and checks that each fails with ErrInvalidKey before reaching the backend.
func checkRejected(t *testing.T, s *stash.Store, spy *spyBackend, key string, delta int64) {
	t.Helper()
	ctx := t.Context()
	ops := []struct {
		name string
		call func() error
	}{
		{"Get", func() error { _, err := s.Get(ctx, key); return err }},
		{"Set", func() error { return s.Set(ctx, key, []byte("v")) }},
		{"Set(IfNotExists)", func() error { return s.Set(ctx, key, []byte("v"), stash.IfNotExists()) }},
		{"Delete", func() error { _, err := s.Delete(ctx, key); return err }},
		{"Exists", func() error { _, err := s.Exists(ctx, key); return err }},
		{"Incr", func() error { _, err := s.Incr(ctx, key, delta); return err }},
		{"TTL", func() error { _, err := s.TTL(ctx, key); return err }},
		{"Expire", func() error { return s.Expire(ctx, key, time.Hour) }},
	}
	for _, op := range ops {
		err := op.call()
		if !errors.Is(err, stash.ErrInvalidKey) {
			t.Errorf("%s err = %v, want ErrInvalidKey", op.name, err)
		}
		if _, ok := errors.AsType[*stash.Error](err); !ok {
			t.Errorf("%s err = %T, want *stash.Error", op.name, err)
		}
	}
	if seen := spy.seen(); len(seen) != 0 {
		t.Errorf("rejected key reached the backend as %q", seen)
	}
}

// pair is one logical location: a namespace path and a key inside it.
type pair struct {
	path []string
	key  string
}

// prefix returns the backend prefix of the pair's namespace path.
func (p pair) prefix() string {
	if len(p.path) == 0 {
		return ""
	}
	return strings.Join(p.path, sep) + sep
}

// id identifies the pair logically, independent of how stash encodes it.
func (p pair) id() string {
	return strconv.Quote(strings.Join(p.path, "\x00")) + strconv.Quote(p.key)
}

// valid reports whether stash accepts the pair's namespaces and key.
func (p pair) valid() bool {
	return !slices.ContainsFunc(p.path, func(n string) bool { return !validName(n) }) && validKey(p.prefix(), p.key)
}

// sepFree reports whether no namespace name or key in the pair contains the
// separator. For such pairs the backend key determines the pair uniquely.
func (p pair) sepFree() bool {
	return !strings.Contains(p.key, sep) && !slices.ContainsFunc(p.path, func(n string) bool { return strings.Contains(n, sep) })
}

// at returns the store for path under root.
func at(root *stash.Store, path []string) *stash.Store {
	s := root
	for _, name := range path {
		s = s.Namespace(name)
	}
	return s
}

// fuzzPairs builds the locations FuzzNamespaceIsolation and FuzzClear
// write: siblings a and b, a nested under a, and the root.
func fuzzPairs(a, b, c, k1, k2 string) []pair {
	return []pair{
		{[]string{a}, k1},
		{[]string{b}, k1},
		{[]string{b}, k2},
		{[]string{a, c}, k2},
		{[]string{a, c}, k1},
		{nil, k1},
	}
}

// fuzzPaths returns the distinct namespace paths in pairs, root first.
func fuzzPaths(pairs []pair) [][]string {
	var paths [][]string
	for _, p := range pairs {
		if !slices.ContainsFunc(paths, func(q []string) bool { return slices.Equal(p.path, q) }) {
			paths = append(paths, p.path)
		}
	}
	return paths
}

// populate writes a distinct value through each pair and returns the backend
// contents the namespace encoding predicts, keyed by full key.
func populate(t *testing.T, root *stash.Store, pairs []pair) map[string]string {
	t.Helper()
	want := map[string]string{}
	for i, p := range pairs {
		v := "v" + strconv.Itoa(i)
		err := at(root, p.path).Set(t.Context(), p.key, []byte(v))
		if !p.valid() {
			if !errors.Is(err, stash.ErrInvalidKey) {
				t.Fatalf("Set(%q in %q) err = %v, want ErrInvalidKey", p.key, p.path, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("Set(%q in %q): %v", p.key, p.path, err)
		}
		want[p.prefix()+p.key] = v
	}
	return want
}

// checkContents checks Get on every pair and Keys on every namespace path
// against want, the predicted backend contents. When every pair is
// separator-free it also checks that each pair reads back the last value
// written through that same pair, which fails if two distinct pairs share a
// backend key.
func checkContents(t *testing.T, root *stash.Store, pairs []pair, want map[string]string, logical map[string]string) {
	t.Helper()
	ctx := t.Context()
	isolated := !slices.ContainsFunc(pairs, func(p pair) bool { return !p.sepFree() })
	for _, p := range pairs {
		if !p.valid() {
			continue
		}
		got, err := at(root, p.path).Get(ctx, p.key)
		v, ok := want[p.prefix()+p.key]
		switch {
		case !ok && !errors.Is(err, stash.ErrNotFound):
			t.Errorf("Get(%q in %q) = %q, %v, want ErrNotFound", p.key, p.path, got, err)
		case ok && (err != nil || string(got) != v):
			t.Errorf("Get(%q in %q) = %q, %v, want %q", p.key, p.path, got, err, v)
		}
		if lv, lok := logical[p.id()]; isolated && (lok != ok || (ok && lv != string(got))) {
			t.Errorf("pair (%q, %q) reads %q, %v, but was last written %q, %v", p.path, p.key, got, ok, lv, lok)
		}
	}
	for _, path := range fuzzPaths(pairs) {
		checkKeys(t, root, path, want)
	}
}

// checkKeys checks that Keys on path yields exactly the keys in want under
// the path's prefix, with the prefix removed.
func checkKeys(t *testing.T, root *stash.Store, path []string, want map[string]string) {
	t.Helper()
	s := at(root, path)
	if slices.ContainsFunc(path, func(n string) bool { return !validName(n) }) {
		for _, err := range s.Keys(t.Context()) {
			if !errors.Is(err, stash.ErrInvalidKey) {
				t.Errorf("Keys on %q err = %v, want ErrInvalidKey", path, err)
			}
		}
		return
	}
	prefix := pair{path: path}.prefix()
	var expect []string
	for full := range want {
		if rest, ok := strings.CutPrefix(full, prefix); ok {
			expect = append(expect, rest)
		}
	}
	slices.Sort(expect)
	got := collectKeys(t, s)
	if !slices.Equal(got, expect) {
		t.Errorf("Keys on %q = %q, want %q", path, got, expect)
	}
}

// collectKeys returns the sorted keys s yields.
func collectKeys(t *testing.T, s *stash.Store) []string {
	t.Helper()
	var got []string
	for k, err := range s.Keys(t.Context()) {
		if err != nil {
			t.Fatalf("Keys: %v", err)
		}
		got = append(got, k)
	}
	slices.Sort(got)
	return got
}

// logicalModel returns the last value written through each valid pair, keyed
// by the pair's logical identity.
func logicalModel(pairs []pair) map[string]string {
	m := map[string]string{}
	for i, p := range pairs {
		if p.valid() {
			m[p.id()] = "v" + strconv.Itoa(i)
		}
	}
	return m
}

// namespaceSeeds adds inputs where names are prefixes of each other, contain
// the separator, nest, or are invalid.
func namespaceSeeds(f *testing.F) {
	f.Helper()
	long := strings.Repeat("n", stash.MaxKeyLen-len(sep+"k"))
	for _, s := range [][5]string{
		{"a", "b", "c", "k", "j"},
		{"a", "a", "a", "k", "k"},
		{"a", "ab", "b", "k", "j"},
		{"ab", "a", "b", "k", "b:k"},
		{"user", "user1", "1", "42", "x"},
		{"a b", "a", "%", "*", "?"},
		{"é", "e", "́", "k", "j"},
		{"a", "b", "", "k", "j"},
		{"a", "\xff", "c", "k", "j"},
		{long, "b", "c", "k", "kk"},
	} {
		f.Add(s[0], s[1], s[2], s[3], s[4])
	}
}

// FuzzNamespaceIsolation checks that values written through namespaces read
// back as the colon encoding predicts, that Keys on each namespace yields
// exactly its keys without the prefix, and that distinct separator-free
// namespace and key pairs never see each other's values.
func FuzzNamespaceIsolation(f *testing.F) {
	namespaceSeeds(f)
	f.Fuzz(func(t *testing.T, a, b, c, k1, k2 string) {
		root := stash.New(newFake())
		pairs := fuzzPairs(a, b, c, k1, k2)
		want := populate(t, root, pairs)
		checkContents(t, root, pairs, want, logicalModel(pairs))
	})
}

// FuzzClear checks that Clear on a namespace removes exactly the keys under
// its prefix, that Clear on the root store fails with ErrInvalidOption and
// removes nothing, and that Clear on an invalid namespace fails with
// ErrInvalidKey.
func FuzzClear(f *testing.F) {
	namespaceSeeds(f)
	f.Fuzz(func(t *testing.T, a, b, c, k1, k2 string) {
		ctx := t.Context()
		root := stash.New(newFake())
		pairs := fuzzPairs(a, b, c, k1, k2)
		want := populate(t, root, pairs)
		logical := logicalModel(pairs)

		if err := root.Clear(ctx); !errors.Is(err, stash.ErrInvalidOption) {
			t.Fatalf("Clear on root err = %v, want ErrInvalidOption", err)
		}
		checkContents(t, root, pairs, want, logical)

		err := root.Namespace(a).Clear(ctx)
		if !validName(a) {
			if !errors.Is(err, stash.ErrInvalidKey) {
				t.Fatalf("Clear on %q err = %v, want ErrInvalidKey", a, err)
			}
			checkContents(t, root, pairs, want, logical)
			return
		}
		if err != nil {
			t.Fatalf("Clear on %q: %v", a, err)
		}
		maps.DeleteFunc(want, func(full, _ string) bool { return strings.HasPrefix(full, a+sep) })
		maps.DeleteFunc(logical, func(id, _ string) bool {
			i := slices.IndexFunc(pairs, func(p pair) bool { return p.id() == id })
			return len(pairs[i].path) > 0 && pairs[i].path[0] == a
		})
		checkContents(t, root, pairs, want, logical)
	})
}

// record is a small struct for the typed round-trip.
type record struct {
	Name  string
	Count int
	Big   int64
	Tags  []string
	Attrs map[string]int
}

// FuzzTypedJSONRoundTrip checks that a record stored through Typed with JSON
// reads back equal. encoding/json replaces invalid UTF-8 in strings, so for
// such inputs it checks only that the round trip succeeds.
func FuzzTypedJSONRoundTrip(f *testing.F) {
	f.Add("ada", 1, int64(1)<<62, "a,b", "k", -3, uint8(3))
	f.Add("", 0, int64(0), "", "", 0, uint8(0))
	f.Add("<&>\"\\ ", -1<<31, int64(-1)<<63, ",,", "\x00", 1, uint8(7))
	f.Add("\xff", 2, int64(2), "\xfe", "k", 2, uint8(3))
	f.Fuzz(func(t *testing.T, name string, count int, big int64, tags, attrKey string, attrVal int, shape uint8) {
		ctx := t.Context()
		in := record{Name: name, Count: count, Big: big}
		switch shape % 3 {
		case 1:
			in.Tags = []string{}
		case 2:
			in.Tags = strings.Split(tags, ",")
		}
		if shape&4 != 0 {
			in.Attrs = map[string]int{attrKey: attrVal, name: count}
		}
		ts := stash.Typed[record](stash.New(newFake()).Namespace("typed"), stash.JSON)
		if err := ts.Set(ctx, "rec", in); err != nil {
			t.Fatalf("Set(%#v): %v", in, err)
		}
		out, err := ts.Get(ctx, "rec")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if utf8.ValidString(name) && utf8.ValidString(tags) && utf8.ValidString(attrKey) && !reflect.DeepEqual(in, out) {
			t.Fatalf("round trip = %#v, want %#v", out, in)
		}
	})
}

// TestNamespaceIsolationProperty writes, deletes, and clears random
// separator-free namespace and key pairs, from a fixed seed, and checks that
// every pair reads back exactly what was last written through it, that Keys
// on every namespace yields exactly the keys written through it and its
// nested namespaces, and that Clear removes only its own namespace.
func TestNamespaceIsolationProperty(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	names := []string{"a", "ab", "b", "ba", "a.b", "a b", "é", "é", "%", "_"}
	keys := []string{"k", "k1", "a", "ab", "b", "é", "*", "?"}
	for round := range 300 {
		ctx := t.Context()
		root := stash.New(newFake())
		model := map[string]pair{} // logical id -> pair
		values := map[string]string{}
		randPair := func() pair {
			path := make([]string, r.IntN(4))
			for i := range path {
				path[i] = names[r.IntN(len(names))]
			}
			return pair{path: path, key: keys[r.IntN(len(keys))]}
		}
		for op := range 1 + r.IntN(30) {
			p := randPair()
			s := at(root, p.path)
			switch n := r.IntN(10); {
			case n < 7:
				v := strconv.Itoa(round) + "/" + strconv.Itoa(op)
				if err := s.Set(ctx, p.key, []byte(v)); err != nil {
					t.Fatalf("round %d: Set(%q in %q): %v", round, p.key, p.path, err)
				}
				model[p.id()], values[p.id()] = p, v
			case n < 9:
				_, had := model[p.id()]
				if ok, err := s.Delete(ctx, p.key); ok != had || err != nil {
					t.Fatalf("round %d: Delete(%q in %q) = %v, %v, want %v", round, p.key, p.path, ok, err, had)
				}
				delete(model, p.id())
			default:
				err := s.Clear(ctx)
				if len(p.path) == 0 {
					if !errors.Is(err, stash.ErrInvalidOption) {
						t.Fatalf("round %d: Clear on root err = %v, want ErrInvalidOption", round, err)
					}
					continue
				}
				if err != nil {
					t.Fatalf("round %d: Clear on %q: %v", round, p.path, err)
				}
				maps.DeleteFunc(model, func(_ string, q pair) bool { return hasPathPrefix(q.path, p.path) })
			}
		}
		checkModel(t, root, model, values, randPair)
	}
}

// hasPathPrefix reports whether path is prefix or a namespace nested in it.
func hasPathPrefix(path, prefix []string) bool {
	return len(path) >= len(prefix) && slices.Equal(path[:len(prefix)], prefix)
}

// checkModel checks every stored pair, a sample of other pairs, and Keys on
// every namespace path in the model against the logical model.
func checkModel(t *testing.T, root *stash.Store, model map[string]pair, values map[string]string, randPair func() pair) {
	t.Helper()
	ctx := t.Context()
	for id, p := range model {
		if got, err := at(root, p.path).Get(ctx, p.key); err != nil || string(got) != values[id] {
			t.Fatalf("Get(%q in %q) = %q, %v, want %q", p.key, p.path, got, err, values[id])
		}
	}
	for range 20 {
		p := randPair()
		if _, ok := model[p.id()]; ok {
			continue
		}
		if got, err := at(root, p.path).Get(ctx, p.key); !errors.Is(err, stash.ErrNotFound) {
			t.Fatalf("Get(%q in %q) of an unset pair = %q, %v, want ErrNotFound", p.key, p.path, got, err)
		}
	}
	paths := [][]string{nil}
	for _, p := range model {
		for i := range len(p.path) + 1 {
			paths = append(paths, p.path[:i])
		}
	}
	for _, path := range paths {
		var expect []string
		for _, p := range model {
			if hasPathPrefix(p.path, path) {
				expect = append(expect, strings.Join(append(slices.Clone(p.path[len(path):]), p.key), sep))
			}
		}
		slices.Sort(expect)
		if got := collectKeys(t, at(root, path)); !slices.Equal(got, expect) {
			t.Fatalf("Keys on %q = %q, want %q", path, got, expect)
		}
	}
}
