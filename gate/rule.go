package gate

import "iter"

// Rule reports whether a value satisfies a condition.
//
// Any func(T) bool is a Rule, and a Rule is called directly to evaluate it.
type Rule[T any] func(T) bool

// And returns a rule that passes when r and every rule in rules pass. It
// evaluates left to right and stops at the first failure.
func (r Rule[T]) And(rules ...Rule[T]) Rule[T] {
	return All(prepend(r, rules)...)
}

// Or returns a rule that passes when r or any rule in rules passes. It
// evaluates left to right and stops at the first pass.
func (r Rule[T]) Or(rules ...Rule[T]) Rule[T] {
	return Any(prepend(r, rules)...)
}

// Not returns a rule that passes when r fails.
func (r Rule[T]) Not() Rule[T] {
	return func(v T) bool {
		return !r(v)
	}
}

// Xor returns a rule that passes when exactly one of r and other passes. It
// always evaluates both.
func (r Rule[T]) Xor(other Rule[T]) Rule[T] {
	return func(v T) bool {
		return r(v) != other(v)
	}
}

// Requires returns a rule that passes when r fails, or when both r and then
// pass: whenever r holds, then must hold too. then is evaluated only when r
// passes.
func (r Rule[T]) Requires(then Rule[T]) Rule[T] {
	return func(v T) bool {
		return !r(v) || then(v)
	}
}

// Filter returns a sequence of the values from seq that pass r. It is lazy:
// values are read from seq and tested only as the result is iterated.
func (r Rule[T]) Filter(seq iter.Seq[T]) iter.Seq[T] {
	return func(yield func(T) bool) {
		for v := range seq {
			if r(v) && !yield(v) {
				return
			}
		}
	}
}

// Else returns a check that reports nil when r passes and err when r fails.
// err should be non-nil; with a nil err the check never reports a failure.
func (r Rule[T]) Else(err error) Check[T] {
	return func(v T) error {
		if r(v) {
			return nil
		}
		return err
	}
}

// prepend returns a new slice holding first followed by rest.
func prepend[T any](first Rule[T], rest []Rule[T]) []Rule[T] {
	return append([]Rule[T]{first}, rest...)
}
