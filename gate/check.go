package gate

import (
	"errors"
	"slices"
)

// Check reports why a value fails a condition, or nil when it satisfies it.
//
// Any func(T) error is a Check; [Rule.Else] builds one from a rule. The
// errors a check reports belong to the caller and are matched with
// [errors.Is] and [errors.As].
type Check[T any] func(T) error

// Join returns a check that runs every check and reports all of their errors
// combined with [errors.Join], or nil when every check passes. Each reported
// error remains matchable with errors.Is and errors.As. Join with no checks
// always reports nil.
func Join[T any](checks ...Check[T]) Check[T] {
	checks = slices.Clone(checks)
	return func(v T) error {
		var errs []error
		for _, check := range checks {
			if err := check(v); err != nil {
				errs = append(errs, err)
			}
		}
		return errors.Join(errs...)
	}
}

// First returns a check that runs checks left to right and reports the first
// error, without running the rest, or nil when every check passes. First
// with no checks always reports nil.
func First[T any](checks ...Check[T]) Check[T] {
	checks = slices.Clone(checks)
	return func(v T) error {
		for _, check := range checks {
			if err := check(v); err != nil {
				return err
			}
		}
		return nil
	}
}
