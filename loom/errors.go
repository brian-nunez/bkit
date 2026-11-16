package loom

import (
	"errors"
	"strconv"
	"strings"
)

// Sentinel errors. Task failures arrive wrapped in *Error; match them with
// errors.Is.
var (
	// ErrInvalidGraph reports a graph that cannot run: a nil graph, a task
	// with an empty or duplicate name or a nil function, a parent from
	// another graph, or a task added after the first Run. Fix the graph
	// where it is built.
	ErrInvalidGraph = errors.New("loom: invalid graph")

	// ErrInvalidOption reports an option value that is out of range, a nil
	// option or context, a Resume result from another graph, or a Pool
	// that was not created with NewPool.
	ErrInvalidOption = errors.New("loom: invalid option")

	// ErrSkip, returned by a task (wrapped or not), marks the task Skipped
	// instead of Failed. Its children follow their trigger rule: under the
	// default AllSuccess they are Skipped too.
	ErrSkip = errors.New("loom: skip")

	// ErrUpstream is the Kind of a task that never ran because its trigger
	// rule saw a failed parent.
	ErrUpstream = errors.New("loom: upstream failed")

	// ErrTimeout is the Kind of an attempt that returned an error after
	// its WithTimeout deadline passed. It is also the cause of the task's
	// context, so context.Cause(ctx) reports it inside the task.
	ErrTimeout = errors.New("loom: task timed out")

	// ErrPanic is the Kind of an attempt that panicked or called
	// runtime.Goexit, and of a task whose trigger rule did.
	ErrPanic = errors.New("loom: task panicked")

	// ErrInterrupted is the Kind of an attempt that returned an error after
	// its worker was interrupted by Shrink with Interrupt, or by a Shutdown
	// deadline. It is also the cause of the task's context. A RetryPolicy
	// that matches it decides whether interrupted work runs again.
	ErrInterrupted = errors.New("loom: worker interrupted")

	// ErrClosed reports a call on a pool after Shutdown or Close, and is
	// the Kind of a task that could not start because its pool closed.
	ErrClosed = errors.New("loom: pool closed")

	// ErrInvalidSize reports a Grow or Shrink count below one, or a Shrink
	// larger than the pool.
	ErrInvalidSize = errors.New("loom: invalid pool size")

	// ErrNestedRun reports a Run whose context comes from a task running on
	// the same pool. The outer task holds a worker while it waits, so the
	// inner run could starve. Give the inner run its own pool, or none.
	ErrNestedRun = errors.New("loom: nested run on the same pool")
)

// errGoexit is the Err of an attempt that called runtime.Goexit.
var errGoexit = errors.New("called runtime.Goexit")

// maxChain bounds how many errors loom visits in one error chain, so an
// error that wraps itself cannot loop forever.
const maxChain = 64

// Error describes a task that did not succeed. It matches Kind and Err with
// errors.Is and errors.As.
type Error struct {
	// Task is the task's name.
	Task string

	// Attempt is the attempt that failed, starting at 1. It is 0 for a task
	// that never started.
	Attempt int

	// Kind is the loom sentinel that classifies the failure (ErrTimeout,
	// ErrPanic, ErrInterrupted, ErrUpstream, ErrClosed), or nil when the
	// task returned an error of its own.
	Kind error

	// Err is the error the task returned, the panic value, or the reason
	// the task was canceled. It can be nil.
	Err error
}

// Error formats the task, the attempt, the kind, and the cause. A cause that
// is the kind itself is left out, and a cause whose Error method panics is
// reported as unprintable.
func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString("loom: task ")
	b.WriteString(strconv.Quote(e.Task))
	if e.Attempt > 0 {
		b.WriteString(" attempt ")
		b.WriteString(strconv.Itoa(e.Attempt))
	}
	if e.Kind != nil {
		b.WriteString(": ")
		b.WriteString(strings.TrimPrefix(e.Kind.Error(), "loom: "))
	}
	if e.Err != nil && any(e.Err) != any(e.Kind) {
		b.WriteString(": ")
		b.WriteString(message(e.Err))
	}
	return b.String()
}

// Unwrap returns Kind and Err, leaving out whichever is nil.
func (e *Error) Unwrap() []error {
	var errs []error
	if e.Kind != nil {
		errs = append(errs, e.Kind)
	}
	if e.Err != nil {
		errs = append(errs, e.Err)
	}
	return errs
}

// message returns err.Error(), or a placeholder when that method panics.
func message(err error) (msg string) {
	defer func() {
		if recover() != nil {
			msg = "unprintable error"
		}
	}()
	return err.Error()
}

// matches reports whether target appears in err's chain. It follows Unwrap
// methods only, visits at most maxChain errors, and reports false when a
// method in the chain panics, so an application error cannot hang or crash
// loom.
func matches(err, target error) (found bool) {
	defer func() {
		if recover() != nil {
			found = false
		}
	}()
	queue := []error{err}
	for visited := 0; len(queue) > 0 && visited < maxChain; visited++ {
		e := queue[0]
		queue = queue[1:]
		if any(e) == any(target) {
			return true
		}
		switch u := any(e).(type) {
		case interface{ Unwrap() error }:
			if next := u.Unwrap(); next != nil {
				queue = append(queue, next)
			}
		case interface{ Unwrap() []error }:
			for _, next := range u.Unwrap() {
				if next != nil {
					queue = append(queue, next)
				}
			}
		}
	}
	return false
}
