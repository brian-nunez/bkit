package loom

import "strconv"

// State is where a task is in a run.
type State int

// Task states. Succeeded, Failed, Skipped, UpstreamFailed, and Canceled are
// final.
const (
	// Pending tasks wait for their parents, or never ran because the run
	// returned first.
	Pending State = iota

	// Running tasks have an attempt queued on the pool or running on a
	// worker.
	Running

	// Retrying tasks failed an attempt and wait for their RetryPolicy
	// delay before the next one.
	Retrying

	// Succeeded tasks returned a nil error. Their value is available to
	// children and to Get on the Result.
	Succeeded

	// Failed tasks returned an error, panicked, or exhausted their retry
	// policy.
	Failed

	// Skipped tasks returned ErrSkip, or their trigger rule declined to run
	// them without any failed parent.
	Skipped

	// UpstreamFailed tasks never ran because their trigger rule declined to
	// run them and a parent failed.
	UpstreamFailed

	// Canceled tasks stopped or never started because the run's context
	// ended, FailFast stopped the run, or the pool closed.
	Canceled
)

// String returns the state's name, such as "succeeded".
func (s State) String() string {
	switch s {
	case Pending:
		return "pending"
	case Running:
		return "running"
	case Retrying:
		return "retrying"
	case Succeeded:
		return "succeeded"
	case Failed:
		return "failed"
	case Skipped:
		return "skipped"
	case UpstreamFailed:
		return "upstream_failed"
	case Canceled:
		return "canceled"
	default:
		return "State(" + strconv.Itoa(int(s)) + ")"
	}
}

// final reports whether s is a state a task does not leave.
func (s State) final() bool {
	return s >= Succeeded
}

// failed reports whether s counts as a failure for trigger rules.
func (s State) failed() bool {
	return s == Failed || s == UpstreamFailed
}

// Trigger decides whether a task with parents runs, once every parent has a
// final state. It receives the parents' states in the order they were
// passed to After. Tasks without parents always run. A trigger that
// declines marks the task UpstreamFailed when a parent failed, and Skipped
// otherwise.
type Trigger func(parents []State) bool

// AllSuccess runs a task when every parent succeeded. It is the default.
func AllSuccess(parents []State) bool {
	for _, s := range parents {
		if s != Succeeded {
			return false
		}
	}
	return true
}

// AllDone runs a task once every parent is done, whatever happened to them.
// Use it for cleanup.
func AllDone([]State) bool {
	return true
}

// AllFailed runs a task when every parent failed or was upstream-failed.
func AllFailed(parents []State) bool {
	for _, s := range parents {
		if !s.failed() {
			return false
		}
	}
	return true
}

// OneSuccess runs a task when at least one parent succeeded.
func OneSuccess(parents []State) bool {
	for _, s := range parents {
		if s == Succeeded {
			return true
		}
	}
	return false
}

// OneFailed runs a task when at least one parent failed or was
// upstream-failed.
func OneFailed(parents []State) bool {
	for _, s := range parents {
		if s.failed() {
			return true
		}
	}
	return false
}

// NoneFailed runs a task when no parent failed or was upstream-failed.
// Skipped parents count as fine.
func NoneFailed(parents []State) bool {
	return !OneFailed(parents)
}
