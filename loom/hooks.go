package loom

import (
	"context"
	"sync"
	"time"
)

// Hooks are callbacks for events. Every field is optional. loom calls hooks
// in event order on a separate goroutine, never on a task's goroutine, so a
// slow or blocked hook delays only later hooks. A hook can run after Run
// returns, and the context it receives can already be canceled; use it for
// its values. A hook that panics or calls runtime.Goexit is recovered and
// later hooks still run.
type Hooks struct {
	// OnRunStart is called when a run starts.
	OnRunStart func(ctx context.Context, e RunEvent)

	// OnRunFinish is called when a run returns.
	OnRunFinish func(ctx context.Context, e RunEvent)

	// OnTaskStart is called when an attempt starts on a worker.
	OnTaskStart func(ctx context.Context, e TaskEvent)

	// OnTaskRetry is called when a failed attempt will run again.
	OnTaskRetry func(ctx context.Context, e TaskEvent)

	// OnTaskFinish is called when a task reaches a final state, including
	// tasks that never started.
	OnTaskFinish func(ctx context.Context, e TaskEvent)

	// OnWorkerStart is called when a pool starts a worker.
	OnWorkerStart func(e WorkerEvent)

	// OnWorkerStop is called when a worker leaves the pool.
	OnWorkerStop func(e WorkerEvent)

	// OnResize is called when Grow or Shrink changes the pool's size.
	OnResize func(e ResizeEvent)
}

// RunEvent describes a run starting or finishing.
type RunEvent struct {
	// Graph is the graph's WithName, or empty.
	Graph string

	// Err is the error Run returns. It is nil on OnRunStart.
	Err error

	// Duration is how long the run took. It is zero on OnRunStart.
	Duration time.Duration
}

// TaskEvent describes a task starting, retrying, or finishing.
type TaskEvent struct {
	// Graph is the graph's WithName, or empty.
	Graph string

	// Task is the task's name.
	Task string

	// State is Running on OnTaskStart, Retrying on OnTaskRetry, and the
	// final state on OnTaskFinish.
	State State

	// Attempt is the attempt number, starting at 1, or 0 for a task that
	// never started.
	Attempt int

	// Err is the task's *Error, or nil.
	Err error

	// Delay is the wait before the next attempt, on OnTaskRetry.
	Delay time.Duration

	// Duration is how long the attempt ran, on OnTaskRetry and
	// OnTaskFinish.
	Duration time.Duration
}

// WorkerEvent describes a worker starting or stopping.
type WorkerEvent struct {
	// Worker identifies the worker within its pool.
	Worker int

	// Interrupted reports, on OnWorkerStop, that the worker's last task was
	// interrupted.
	Interrupted bool
}

// ResizeEvent describes a change in a pool's size.
type ResizeEvent struct {
	// From is the size before the change.
	From int

	// To is the size after the change.
	To int
}

// dispatcher delivers callbacks one at a time, in the order they were
// emitted, on a goroutine of its own. The goroutine exits when the queue is
// empty and starts again on the next emit.
type dispatcher struct {
	mu      sync.Mutex
	queue   []func()
	running bool
}

// emit queues f and returns without waiting for it.
func (d *dispatcher) emit(f func()) {
	d.mu.Lock()
	d.queue = append(d.queue, f)
	if d.running {
		d.mu.Unlock()
		return
	}
	d.running = true
	d.mu.Unlock()
	go d.drain()
}

func (d *dispatcher) drain() {
	for {
		d.mu.Lock()
		if len(d.queue) == 0 {
			d.running = false
			d.mu.Unlock()
			return
		}
		f := d.queue[0]
		d.queue = d.queue[1:]
		d.mu.Unlock()
		isolate(f)
	}
}

// taskHook picks one task callback from Hooks.
type taskHook func(h Hooks) func(context.Context, TaskEvent)

func onTaskStart(h Hooks) func(context.Context, TaskEvent)  { return h.OnTaskStart }
func onTaskRetry(h Hooks) func(context.Context, TaskEvent)  { return h.OnTaskRetry }
func onTaskFinish(h Hooks) func(context.Context, TaskEvent) { return h.OnTaskFinish }

// runHook picks one run callback from Hooks.
type runHook func(h Hooks) func(context.Context, RunEvent)

func onRunStart(h Hooks) func(context.Context, RunEvent)  { return h.OnRunStart }
func onRunFinish(h Hooks) func(context.Context, RunEvent) { return h.OnRunFinish }

// workerHook picks one worker callback from Hooks.
type workerHook func(h Hooks) func(WorkerEvent)

func onWorkerStart(h Hooks) func(WorkerEvent) { return h.OnWorkerStart }
func onWorkerStop(h Hooks) func(WorkerEvent)  { return h.OnWorkerStop }

// emitTask queues a task event for every hook set that has the callback.
func (d *dispatcher) emitTask(ctx context.Context, hooks []Hooks, pick taskHook, e TaskEvent) {
	for _, h := range hooks {
		if f := pick(h); f != nil {
			d.emit(func() { f(ctx, e) })
		}
	}
}

// emitRun queues a run event for every hook set that has the callback.
func (d *dispatcher) emitRun(ctx context.Context, hooks []Hooks, pick runHook, e RunEvent) {
	for _, h := range hooks {
		if f := pick(h); f != nil {
			d.emit(func() { f(ctx, e) })
		}
	}
}

// emitWorker queues a worker event for every hook set that has the callback.
func (d *dispatcher) emitWorker(hooks []Hooks, pick workerHook, e WorkerEvent) {
	for _, h := range hooks {
		if f := pick(h); f != nil {
			d.emit(func() { f(e) })
		}
	}
}

// emitResize queues a resize event for every hook set that has OnResize.
func (d *dispatcher) emitResize(hooks []Hooks, e ResizeEvent) {
	for _, h := range hooks {
		if h.OnResize != nil {
			f := h.OnResize
			d.emit(func() { f(e) })
		}
	}
}
