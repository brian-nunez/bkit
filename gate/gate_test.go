package gate

import (
	"errors"
	"slices"
	"testing"
)

var (
	pass Rule[int] = func(int) bool { return true }
	fail Rule[int] = func(int) bool { return false }
	even Rule[int] = func(v int) bool { return v%2 == 0 }
)

// recorder logs which named rules and checks ran, in order.
type recorder struct {
	calls []string
}

func (r *recorder) rule(name string, result bool) Rule[int] {
	return func(int) bool {
		r.calls = append(r.calls, name)
		return result
	}
}

func (r *recorder) check(name string, err error) Check[int] {
	return func(int) error {
		r.calls = append(r.calls, name)
		return err
	}
}

func assertCalls(t *testing.T, rec *recorder, want ...string) {
	t.Helper()
	if !slices.Equal(rec.calls, want) {
		t.Fatalf("calls = %v, want %v", rec.calls, want)
	}
}

func TestBooleanCombinators(t *testing.T) {
	tests := []struct {
		name string
		rule Rule[int]
		want bool
	}{
		{"and all pass", pass.And(pass, pass), true},
		{"and one fails", pass.And(pass, fail), false},
		{"and receiver fails", fail.And(pass), false},
		{"and alone", pass.And(), true},
		{"or one passes", fail.Or(fail, pass), true},
		{"or all fail", fail.Or(fail), false},
		{"or alone", fail.Or(), false},
		{"not pass", pass.Not(), false},
		{"not fail", fail.Not(), true},
		{"xor pass fail", pass.Xor(fail), true},
		{"xor fail pass", fail.Xor(pass), true},
		{"xor pass pass", pass.Xor(pass), false},
		{"xor fail fail", fail.Xor(fail), false},
		{"requires condition fails", fail.Requires(fail), true},
		{"requires both pass", pass.Requires(pass), true},
		{"requires then fails", pass.Requires(fail), false},
		{"all empty", All[int](), true},
		{"all pass", All(pass, pass), true},
		{"all one fails", All(pass, fail), false},
		{"any empty", Any[int](), false},
		{"any one passes", Any(fail, pass), true},
		{"any all fail", Any(fail, fail), false},
		{"none empty", None[int](), true},
		{"none all fail", None(fail, fail), true},
		{"none one passes", None(fail, pass), false},
		{"at least zero empty", AtLeast[int](0), true},
		{"at least negative", AtLeast(-1, fail), true},
		{"at least zero with rules", AtLeast(0, fail, fail), true},
		{"at least one empty", AtLeast[int](1), false},
		{"at least exceeds rules", AtLeast(3, pass, pass), false},
		{"at least reached", AtLeast(2, pass, fail, pass), true},
		{"at least all needed", AtLeast(2, pass, pass), true},
		{"at least missed", AtLeast(2, pass, fail, fail), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.rule(0); got != tt.want {
				t.Fatalf("rule(0) = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestShortCircuit(t *testing.T) {
	tests := []struct {
		name  string
		build func(*recorder) Rule[int]
		want  []string
	}{
		{"and stops at first failure", func(r *recorder) Rule[int] {
			return r.rule("a", true).And(r.rule("b", false), r.rule("c", true))
		}, []string{"a", "b"}},
		{"or stops at first pass", func(r *recorder) Rule[int] {
			return r.rule("a", false).Or(r.rule("b", true), r.rule("c", true))
		}, []string{"a", "b"}},
		{"none stops at first pass", func(r *recorder) Rule[int] {
			return None(r.rule("a", true), r.rule("b", false))
		}, []string{"a"}},
		{"xor evaluates both", func(r *recorder) Rule[int] {
			return r.rule("a", true).Xor(r.rule("b", true))
		}, []string{"a", "b"}},
		{"requires skips then when condition fails", func(r *recorder) Rule[int] {
			return r.rule("a", false).Requires(r.rule("b", true))
		}, []string{"a"}},
		{"at least stops once reached", func(r *recorder) Rule[int] {
			return AtLeast(2, r.rule("a", true), r.rule("b", true), r.rule("c", true))
		}, []string{"a", "b"}},
		{"at least stops once unreachable", func(r *recorder) Rule[int] {
			return AtLeast(2, r.rule("a", false), r.rule("b", false), r.rule("c", true))
		}, []string{"a", "b"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := &recorder{}
			tt.build(rec)(0)
			assertCalls(t, rec, tt.want...)
		})
	}
}

func TestCombinatorsCopyArguments(t *testing.T) {
	rules := []Rule[int]{pass, pass}
	checks := []Check[int]{pass.Else(errA)}

	all := All(rules...)
	anyRule := Any(rules...)
	atLeast := AtLeast(2, rules...)
	and := pass.And(rules...)
	join := Join(checks...)
	first := First(checks...)

	rules[0], rules[1] = fail, fail
	checks[0] = fail.Else(errA)

	if !all(0) || !anyRule(0) || !atLeast(0) || !and(0) {
		t.Fatal("rule changed after the caller's slice was modified")
	}
	if join(0) != nil || first(0) != nil {
		t.Fatal("check changed after the caller's slice was modified")
	}
}

func TestFilter(t *testing.T) {
	got := slices.Collect(even.Filter(slices.Values([]int{1, 2, 3, 4, 5, 6})))
	if want := []int{2, 4, 6}; !slices.Equal(got, want) {
		t.Fatalf("Filter = %v, want %v", got, want)
	}

	var seen []int
	for v := range even.Filter(slices.Values([]int{2, 3, 4, 6})) {
		seen = append(seen, v)
		if v == 4 {
			break
		}
	}
	if want := []int{2, 4}; !slices.Equal(seen, want) {
		t.Fatalf("Filter with break = %v, want %v", seen, want)
	}
}

var (
	errA = errors.New("a failed")
	errB = errors.New("b failed")
)

func TestElse(t *testing.T) {
	if err := pass.Else(errA)(0); err != nil {
		t.Fatalf("passing rule reported %v", err)
	}
	if err := fail.Else(errA)(0); !errors.Is(err, errA) {
		t.Fatalf("failing rule reported %v, want %v", err, errA)
	}
	if err := fail.Else(nil)(0); err != nil {
		t.Fatalf("nil error reported %v", err)
	}
}

func TestJoin(t *testing.T) {
	if err := Join[int]()(0); err != nil {
		t.Fatalf("empty Join reported %v", err)
	}

	rec := &recorder{}
	check := Join(rec.check("a", errA), rec.check("b", nil), rec.check("c", errB))
	err := check(0)
	assertCalls(t, rec, "a", "b", "c")
	if !errors.Is(err, errA) || !errors.Is(err, errB) {
		t.Fatalf("Join reported %v, want both %v and %v", err, errA, errB)
	}
	if want := "a failed\nb failed"; err.Error() != want {
		t.Fatalf("Join message = %q, want %q", err.Error(), want)
	}

	if err := Join(pass.Else(errA), pass.Else(errB))(0); err != nil {
		t.Fatalf("passing Join reported %v", err)
	}
}

func TestFirst(t *testing.T) {
	if err := First[int]()(0); err != nil {
		t.Fatalf("empty First reported %v", err)
	}

	rec := &recorder{}
	err := First(rec.check("a", nil), rec.check("b", errB), rec.check("c", errA))(0)
	assertCalls(t, rec, "a", "b")
	if !errors.Is(err, errB) || errors.Is(err, errA) {
		t.Fatalf("First reported %v, want only %v", err, errB)
	}

	if err := First(pass.Else(errA), pass.Else(errB))(0); err != nil {
		t.Fatalf("passing First reported %v", err)
	}
}
