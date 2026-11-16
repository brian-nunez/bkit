package loom

import "time"

// Attempt describes a failed attempt, passed to a RetryPolicy.
type Attempt struct {
	// Task is the task's name.
	Task string

	// N is the attempt that failed, starting at 1.
	N int

	// Err is the attempt's *Error. errors.Is(Err, ErrInterrupted) reports
	// an interrupted worker, and errors.Is(Err, ErrTimeout) a timeout.
	Err error
}

// RetryPolicy decides whether a failed attempt runs again, and after what
// delay. loom has no retry behavior of its own: a task with no policy is
// not retried. A delay below zero counts as zero. A policy that panics
// counts as no retry.
type RetryPolicy func(a Attempt) (delay time.Duration, retry bool)

// Backoff returns a policy that retries a task up to retries times. The
// first retry waits initial, and each one after doubles the wait, up to
// ceiling. Pass zero for initial to retry immediately.
func Backoff(retries int, initial, ceiling time.Duration) RetryPolicy {
	return func(a Attempt) (time.Duration, bool) {
		if a.N > retries {
			return 0, false
		}
		delay := initial
		for i := 1; i < a.N; i++ {
			if delay >= ceiling/2 {
				delay = ceiling
				break
			}
			delay *= 2
		}
		return min(delay, ceiling), true
	}
}
