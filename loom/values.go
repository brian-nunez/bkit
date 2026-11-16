package loom

// Values holds task values. *Inputs, inside a task, and *Result, after a
// run, both satisfy it, so Get and Lookup work in either place.
type Values interface {
	value(n *node) (any, bool)
}

// Inputs gives a running task the values of its parents that succeeded. It
// is read-only and safe to use from any goroutine, during or after the task.
type Inputs struct {
	values map[*node]any
}

func (in *Inputs) value(n *node) (any, bool) {
	if in == nil {
		return nil, false
	}
	v, ok := in.values[n]
	return v, ok
}

// Result is the outcome of one Run. It is never nil, so a failed or
// canceled run still reports each task's state. A Result is read-only and
// safe for concurrent use.
type Result struct {
	graph    *Graph
	order    []*node
	outcomes map[*node]*outcome
}

// outcome is one task's state, value, and error in a run.
type outcome struct {
	state State
	value any
	err   error
}

func (r *Result) value(n *node) (any, bool) {
	o := r.outcome(n)
	if o == nil || o.state != Succeeded {
		return nil, false
	}
	return o.value, true
}

func (r *Result) outcome(n *node) *outcome {
	if r == nil {
		return nil
	}
	return r.outcomes[n]
}

func refNode(t Ref) *node {
	if t == nil {
		return nil
	}
	return t.node()
}

// State reports t's state in the run. A task that is not in the run's graph
// reports Pending.
func (r *Result) State(t Ref) State {
	if o := r.outcome(refNode(t)); o != nil {
		return o.state
	}
	return Pending
}

// Err reports why t did not succeed: its *Error for Failed, UpstreamFailed,
// and Canceled tasks, and nil otherwise.
func (r *Result) Err(t Ref) error {
	if o := r.outcome(refNode(t)); o != nil {
		return o.err
	}
	return nil
}

// Lookup returns t's value and true when t succeeded and v holds it. Inside
// a task, v holds only parents declared with After that succeeded. It
// returns the zero value and false otherwise, including for a nil v or t.
func Lookup[T any](v Values, t *Task[T]) (T, bool) {
	var zero T
	if v == nil {
		return zero, false
	}
	raw, ok := v.value(t.node())
	if !ok {
		return zero, false
	}
	val, _ := raw.(T)
	return val, true
}

// Get returns t's value, or the zero value when Lookup would report false.
// Under the default AllSuccess trigger every parent succeeded, so Get is
// all a task needs. Use Lookup with other triggers.
func Get[T any](v Values, t *Task[T]) T {
	val, _ := Lookup(v, t)
	return val
}
