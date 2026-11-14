package gate

import "slices"

// All returns a rule that passes when every rule passes. It evaluates left to
// right and stops at the first failure. All with no rules always passes.
func All[T any](rules ...Rule[T]) Rule[T] {
	rules = slices.Clone(rules)
	return func(v T) bool {
		for _, rule := range rules {
			if !rule(v) {
				return false
			}
		}
		return true
	}
}

// Any returns a rule that passes when at least one rule passes. It evaluates
// left to right and stops at the first pass. Any with no rules always fails.
func Any[T any](rules ...Rule[T]) Rule[T] {
	rules = slices.Clone(rules)
	return func(v T) bool {
		for _, rule := range rules {
			if rule(v) {
				return true
			}
		}
		return false
	}
}

// None returns a rule that passes when no rule passes. It evaluates left to
// right and stops at the first pass. None with no rules always passes.
func None[T any](rules ...Rule[T]) Rule[T] {
	return Any(rules...).Not()
}

// AtLeast returns a rule that passes when at least n rules pass. It evaluates
// left to right and stops as soon as n rules have passed or too few rules
// remain to reach n. AtLeast always passes when n <= 0 and always fails when
// n exceeds the number of rules.
func AtLeast[T any](n int, rules ...Rule[T]) Rule[T] {
	rules = slices.Clone(rules)
	return func(v T) bool {
		need := n
		for i, rule := range rules {
			if need <= 0 {
				return true
			}
			if len(rules)-i < need {
				return false
			}
			if rule(v) {
				need--
			}
		}
		return need <= 0
	}
}
