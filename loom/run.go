package loom

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"time"
)

// poolKey marks a task's context with the pool it runs on, so a nested Run
// on the same pool can be refused.
type poolKey struct{}

// Run executes the graph and returns when every task has a final state, or
// when ctx ends. Each task starts as soon as its parents are done and its
// trigger rule agrees, on the pool from WithPool or on a private pool.
//
// The Result is never nil. The error joins every Failed task's *Error in
// the order tasks were added, then context.Cause(ctx) when ctx ended the
// run, then ErrClosed when the pool closed under it. Graph and option
// mistakes fail the run before any task starts. A task that ignores its
// context keeps its worker until it returns, but Run still returns when ctx
// ends.
func (g *Graph) Run(ctx context.Context, opts ...RunOption) (*Result, error) {
	res := &Result{graph: g, outcomes: map[*node]*outcome{}}
	var cfg runConfig
	for _, o := range opts {
		if o == nil {
			cfg.add(invalid("nil run option"))
			continue
		}
		o.applyRun(&cfg)
	}
	if g == nil {
		return res, fmt.Errorf("%w: nil graph", ErrInvalidGraph)
	}
	if ctx == nil {
		return res, invalid("nil context")
	}

	g.mu.Lock()
	g.frozen = true
	nodes := slices.Clone(g.nodes)
	gcfg := g.cfg
	errs := slices.Concat(gcfg.problems, g.errs, cfg.problems)
	g.mu.Unlock()

	pool := cmp.Or(cfg.pool, gcfg.pool)
	if pool != nil {
		if err := pool.usable(); err != nil {
			errs = append(errs, err)
		} else if outer, _ := ctx.Value(poolKey{}).(*Pool); outer == pool {
			errs = append(errs, ErrNestedRun)
		}
	}
	if cfg.resume != nil && cfg.resume.graph != g {
		errs = append(errs, invalid("Resume needs a result from this graph"))
	}
	if len(errs) > 0 {
		return res, errors.Join(errs...)
	}
	if pool == nil {
		pool, _ = NewPool()
		defer stopPrivate(pool)
	}

	runCtx, cancel := context.WithCancelCause(ctx)
	r := &run{
		name:     gcfg.name,
		pool:     pool,
		hooks:    slices.Concat(gcfg.hooks, cfg.hooks, pool.hooks),
		failFast: gcfg.failFast || cfg.failFast,
		defaults: gcfg.defaults,
		caller:   ctx,
		ctx:      runCtx,
		cancel:   cancel,
		res:      res,
		children: map[*node][]*node{},
		waiting:  map[*node]int{},
		results:  make(chan attemptResult),
		retries:  make(chan retryRequest),
		done:     make(chan struct{}),
	}
	res.order = nodes
	return r.execute(cfg.resume)
}

// stopPrivate shuts a run's private pool down without waiting for tasks
// that ignore their context.
func stopPrivate(p *Pool) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = p.Shutdown(ctx)
}

// run is the state of one Run. Only the goroutine that called Run touches
// res, children, waiting, inflight, and the flags; workers report back over
// results.
type run struct {
	name     string
	pool     *Pool
	hooks    []Hooks
	events   dispatcher
	failFast bool
	defaults settings
	caller   context.Context
	ctx      context.Context
	cancel   context.CancelCauseFunc
	res      *Result

	children map[*node][]*node
	waiting  map[*node]int
	inflight int
	stopping bool
	canceled bool // a task was canceled because the caller's context ended
	closed   bool // a task could not start because the pool closed

	results chan attemptResult
	retries chan retryRequest
	done    chan struct{}
}

// attemptResult reports how one attempt ended.
type attemptResult struct {
	n        *node
	attempt  int
	value    any
	err      error
	skip     bool
	canceled bool // the run stopped before the attempt started
	closed   bool // the pool closed before the attempt started
	duration time.Duration
}

// retryRequest asks the run to start the next attempt of a task.
type retryRequest struct {
	n       *node
	attempt int
}

func (r *run) execute(prev *Result) (*Result, error) {
	start := time.Now()
	r.events.emitRun(r.caller, r.hooks, onRunStart, RunEvent{Graph: r.name})
	defer close(r.done)
	defer r.cancel(nil)

	for _, n := range r.res.order {
		o := &outcome{state: Pending}
		if p := prev.outcome(n); p != nil && (p.state == Succeeded || p.state == Skipped) {
			o.state, o.value = p.state, p.value
		}
		r.res.outcomes[n] = o
		for _, p := range n.parents {
			r.children[p] = append(r.children[p], n)
		}
	}
	var roots []*node
	for _, n := range r.res.order {
		if r.res.outcomes[n].state.final() {
			continue
		}
		for _, p := range n.parents {
			if !r.res.outcomes[p].state.final() {
				r.waiting[n]++
			}
		}
		if r.waiting[n] == 0 {
			roots = append(roots, n)
		}
	}
	for _, n := range roots {
		r.ready(n)
	}

	for r.inflight > 0 {
		select {
		case <-r.caller.Done():
			r.abandon()
		case a := <-r.results:
			r.inflight--
			r.handle(a)
		case q := <-r.retries:
			r.inflight--
			r.retry(q)
		}
	}

	var errs []error
	for _, n := range r.res.order {
		if o := r.res.outcomes[n]; o.state == Failed {
			errs = append(errs, o.err)
		}
	}
	if r.canceled {
		errs = append(errs, context.Cause(r.caller))
	}
	if r.closed {
		errs = append(errs, ErrClosed)
	}
	err := errors.Join(errs...)
	r.events.emitRun(r.caller, r.hooks, onRunFinish, RunEvent{Graph: r.name, Err: err, Duration: time.Since(start)})
	return r.res, err
}

// ready decides what happens to a task whose parents are all done.
func (r *run) ready(n *node) {
	if r.stopping {
		r.cancelTask(n, 0, &Error{Task: n.name, Err: context.Cause(r.ctx)})
		return
	}
	if len(n.parents) > 0 {
		states := make([]State, len(n.parents))
		upstreamFailed := false
		for i, p := range n.parents {
			states[i] = r.res.outcomes[p].state
			upstreamFailed = upstreamFailed || states[i].failed()
		}
		trigger := r.defaults.over(n.settings).trigger
		if trigger == nil {
			trigger = AllSuccess
		}
		var ok bool
		end, value := isolate(func() { ok = trigger(states) })
		switch {
		case end != returned:
			r.finish(n, Failed, nil, &Error{Task: n.name, Kind: ErrPanic, Err: panicError(end, value)}, 0, 0)
			return
		case !ok && upstreamFailed:
			r.finish(n, UpstreamFailed, nil, &Error{Task: n.name, Kind: ErrUpstream}, 0, 0)
			return
		case !ok:
			r.finish(n, Skipped, nil, nil, 0, 0)
			return
		}
	}
	r.submit(n, 1)
}

// submit queues an attempt of n on the pool.
func (r *run) submit(n *node, attempt int) {
	s := r.defaults.over(n.settings)
	in := &Inputs{values: map[*node]any{}}
	for _, p := range n.parents {
		if o := r.res.outcomes[p]; o.state == Succeeded {
			in.values[p] = o.value
		}
	}
	j := &job{
		ctx:    r.ctx,
		exec:   func(ctx context.Context) { r.send(r.attempt(ctx, n, attempt, s, in)) },
		reject: func(error) { r.send(attemptResult{n: n, attempt: attempt, closed: true}) },
	}
	if err := r.pool.submit(j); err != nil {
		r.closeTask(n, attempt-1)
		return
	}
	r.res.outcomes[n].state = Running
	r.inflight++
}

// send reports an attempt, or drops it when Run has already returned.
func (r *run) send(a attemptResult) {
	select {
	case r.results <- a:
	case <-r.done:
	}
}

// attempt runs one attempt of n. It runs on a worker goroutine.
func (r *run) attempt(ctx context.Context, n *node, attempt int, s settings, in *Inputs) attemptResult {
	a := attemptResult{n: n, attempt: attempt}
	if r.ctx.Err() != nil {
		a.canceled = true
		return a
	}
	ctx = context.WithValue(ctx, poolKey{}, r.pool)
	if s.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeoutCause(ctx, s.timeout, ErrTimeout)
		defer cancel()
	}
	r.events.emitTask(r.caller, r.hooks, onTaskStart, TaskEvent{Graph: r.name, Task: n.name, State: Running, Attempt: attempt})
	start := time.Now()
	var value any
	var err error
	end, recovered := isolate(func() { value, err = n.run(ctx, in) })
	a.duration = time.Since(start)
	switch {
	case end != returned:
		a.err = &Error{Task: n.name, Attempt: attempt, Kind: ErrPanic, Err: panicError(end, recovered)}
	case err == nil:
		a.value = value
	case matches(err, ErrSkip):
		a.skip = true
	default:
		a.err = &Error{Task: n.name, Attempt: attempt, Kind: kindOf(ctx), Err: err}
	}
	return a
}

// kindOf classifies an attempt's error by why its context ended.
func kindOf(ctx context.Context) error {
	cause := context.Cause(ctx)
	switch {
	case matches(cause, ErrTimeout):
		return ErrTimeout
	case matches(cause, ErrInterrupted):
		return ErrInterrupted
	}
	return nil
}

// panicError describes a recovered panic or runtime.Goexit.
func panicError(end ending, value any) error {
	if end == exited {
		return errGoexit
	}
	if err, ok := value.(error); ok {
		return err
	}
	return fmt.Errorf("%v", value)
}

// handle applies one attempt's outcome.
func (r *run) handle(a attemptResult) {
	switch {
	case a.closed:
		r.closeTask(a.n, a.attempt-1)
	case a.canceled:
		r.cancelTask(a.n, a.attempt-1, &Error{Task: a.n.name, Attempt: a.attempt - 1, Err: context.Cause(r.ctx)})
	case a.skip:
		r.finish(a.n, Skipped, nil, nil, a.attempt, a.duration)
	case a.err == nil:
		r.finish(a.n, Succeeded, a.value, nil, a.attempt, a.duration)
	case r.stopping:
		r.cancelTask(a.n, a.attempt, a.err)
	default:
		r.fail(a)
	}
}

// fail asks the task's retry policy about a failed attempt, and retries or
// fails the task.
func (r *run) fail(a attemptResult) {
	if policy := r.defaults.over(a.n.settings).retry; policy != nil {
		var delay time.Duration
		var again bool
		end, _ := isolate(func() { delay, again = policy(Attempt{Task: a.n.name, N: a.attempt, Err: a.err}) })
		if end == returned && again {
			delay = max(delay, 0)
			r.res.outcomes[a.n].state = Retrying
			r.events.emitTask(r.caller, r.hooks, onTaskRetry, TaskEvent{
				Graph: r.name, Task: a.n.name, State: Retrying, Attempt: a.attempt,
				Err: a.err, Delay: delay, Duration: a.duration,
			})
			r.inflight++
			go r.wait(delay, retryRequest{n: a.n, attempt: a.attempt + 1})
			return
		}
	}
	r.finish(a.n, Failed, nil, a.err, a.attempt, a.duration)
}

// wait sends q after delay, or as soon as the run stops.
func (r *run) wait(delay time.Duration, q retryRequest) {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-r.ctx.Done():
	}
	select {
	case r.retries <- q:
	case <-r.done:
	}
}

// retry starts the next attempt of a task, unless the run is stopping.
func (r *run) retry(q retryRequest) {
	if r.stopping {
		r.cancelTask(q.n, q.attempt-1, &Error{Task: q.n.name, Attempt: q.attempt - 1, Err: context.Cause(r.ctx)})
		return
	}
	r.submit(q.n, q.attempt)
}

// abandon ends a run whose caller's context ended: every task without a
// final state is Canceled, and Run stops waiting for running attempts.
func (r *run) abandon() {
	r.stopping = true
	r.inflight = 0
	cause := context.Cause(r.caller)
	for _, n := range r.res.order {
		if !r.res.outcomes[n].state.final() {
			r.cancelTask(n, 0, &Error{Task: n.name, Err: cause})
		}
	}
}

// cancelTask marks n Canceled.
func (r *run) cancelTask(n *node, attempt int, err error) {
	if r.caller.Err() != nil {
		r.canceled = true
	}
	r.finish(n, Canceled, nil, err, attempt, 0)
}

// closeTask marks n Canceled because its pool closed.
func (r *run) closeTask(n *node, attempt int) {
	r.closed = true
	r.finish(n, Canceled, nil, &Error{Task: n.name, Attempt: attempt, Kind: ErrClosed}, attempt, 0)
}

// finish gives n its final state and readies the children it unblocks.
func (r *run) finish(n *node, state State, value any, err error, attempt int, d time.Duration) {
	o := r.res.outcomes[n]
	o.state, o.value, o.err = state, value, err
	if state == Failed && r.failFast && !r.stopping {
		r.stopping = true
		r.cancel(err)
	}
	r.events.emitTask(r.caller, r.hooks, onTaskFinish, TaskEvent{
		Graph: r.name, Task: n.name, State: state, Attempt: attempt, Err: err, Duration: d,
	})
	for _, c := range r.children[n] {
		r.waiting[c]--
		if r.waiting[c] == 0 {
			r.ready(c)
		}
	}
}
