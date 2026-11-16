package loom

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"sync"
)

// Pool is an elastic set of worker goroutines that runs the tasks of any
// number of graphs and runs. Grow and Shrink change its size while it runs.
//
// Create a Pool with NewPool; its zero value is not usable, and its methods
// return ErrInvalidOption. A Pool is safe for concurrent use.
type Pool struct {
	ready  bool
	hooks  []Hooks
	events dispatcher

	mu      sync.Mutex
	cond    *sync.Cond
	queue   []*job
	live    map[*worker]struct{} // every worker goroutine still running
	size    int                  // live workers not told to stop
	nextID  int
	closed  bool
	running sync.WaitGroup
}

// worker is one worker goroutine.
type worker struct {
	id          int
	stop        bool
	busy        bool
	interrupted bool
	cancel      context.CancelCauseFunc
}

// rank orders workers for Shrink: idle workers before busy ones.
func (w *worker) rank() int {
	if w.busy {
		return 1
	}
	return 0
}

// job is one unit of work a run hands to a pool.
type job struct {
	ctx    context.Context
	exec   func(ctx context.Context)
	reject func(err error)
}

// NewPool starts a pool with runtime.GOMAXPROCS(0) workers, or the count
// given with WithWorkers. Invalid options fail with ErrInvalidOption.
func NewPool(opts ...PoolOption) (*Pool, error) {
	cfg := poolConfig{workers: defaultWorkers()}
	for _, o := range opts {
		if o == nil {
			cfg.add(invalid("nil pool option"))
			continue
		}
		o.applyPool(&cfg)
	}
	if len(cfg.problems) > 0 {
		return nil, errors.Join(cfg.problems...)
	}
	p := &Pool{ready: true, hooks: cfg.hooks, live: map[*worker]struct{}{}}
	p.cond = sync.NewCond(&p.mu)
	p.mu.Lock()
	p.spawn(cfg.workers)
	p.mu.Unlock()
	return p, nil
}

// usable reports ErrInvalidOption for a nil or zero-value pool.
func (p *Pool) usable() error {
	if p == nil || !p.ready {
		return invalid("pool was not created with NewPool")
	}
	return nil
}

// Size reports how many workers the pool has, not counting workers that
// were removed and are finishing a task. It is 0 for a nil or zero-value
// pool.
func (p *Pool) Size() int {
	if p.usable() != nil {
		return 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.size
}

// Grow adds n workers, which start taking work at once. It fails with
// ErrInvalidSize when n is below one, and ErrClosed after Shutdown.
func (p *Pool) Grow(n int) error {
	if err := p.usable(); err != nil {
		return err
	}
	if n < 1 {
		return ErrInvalidSize
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return ErrClosed
	}
	from := p.size
	p.spawn(n)
	p.events.emitResize(p.hooks, ResizeEvent{From: from, To: p.size})
	return nil
}

// Shrink removes n workers and returns without waiting for them. Idle
// workers leave first. A busy worker leaves after its current task, or,
// with Interrupt, cancels that task's context with the cause
// ErrInterrupted and leaves when the task returns. Shrink to zero pauses
// the pool: queued work waits for Grow. It fails with ErrInvalidSize when n
// is below one or above Size, and ErrClosed after Shutdown.
func (p *Pool) Shrink(n int, opts ...ShrinkOption) error {
	if err := p.usable(); err != nil {
		return err
	}
	var cfg shrinkConfig
	for _, o := range opts {
		if o != nil {
			o.applyShrink(&cfg)
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return ErrClosed
	}
	if n < 1 || n > p.size {
		return ErrInvalidSize
	}
	candidates := make([]*worker, 0, p.size)
	for w := range p.live {
		if !w.stop {
			candidates = append(candidates, w)
		}
	}
	// Idle workers first, then oldest first, so the choice is predictable.
	slices.SortFunc(candidates, func(a, b *worker) int {
		return cmp.Or(cmp.Compare(a.rank(), b.rank()), cmp.Compare(a.id, b.id))
	})
	from := p.size
	for _, w := range candidates[:n] {
		w.stop = true
		if w.busy && cfg.interrupt {
			w.interrupted = true
			w.cancel(ErrInterrupted)
		}
	}
	p.size -= n
	p.cond.Broadcast()
	p.events.emitResize(p.hooks, ResizeEvent{From: from, To: p.size})
	return nil
}

// Shutdown stops the pool. Queued tasks that have not started end Canceled
// with ErrClosed, running tasks keep going, and Shutdown waits for every
// worker to leave. When ctx ends first, Shutdown cancels the running tasks'
// contexts with the cause ErrInterrupted and returns ctx.Err() without
// waiting for them to return. Calling Shutdown again waits again.
func (p *Pool) Shutdown(ctx context.Context) error {
	if err := p.usable(); err != nil {
		return err
	}
	if ctx == nil {
		return invalid("nil context")
	}
	p.mu.Lock()
	rejected := p.queue
	p.queue = nil
	p.closed = true
	p.cond.Broadcast()
	p.mu.Unlock()
	for _, j := range rejected {
		j.reject(ErrClosed)
	}

	done := make(chan struct{})
	go func() {
		p.running.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		p.mu.Lock()
		for w := range p.live {
			if w.busy {
				w.interrupted = true
				w.cancel(ErrInterrupted)
			}
		}
		p.mu.Unlock()
		return ctx.Err()
	}
}

// Close is Shutdown with no deadline: it waits for every running task to
// return. A task that never returns makes Close wait forever; use Shutdown
// with a deadline when tasks may ignore their context.
func (p *Pool) Close() error {
	return p.Shutdown(context.Background())
}

// spawn starts n workers. The caller holds p.mu.
func (p *Pool) spawn(n int) {
	for range n {
		w := &worker{id: p.nextID}
		p.nextID++
		p.live[w] = struct{}{}
		p.size++
		p.running.Add(1)
		p.events.emitWorker(p.hooks, onWorkerStart, WorkerEvent{Worker: w.id})
		go p.loop(w)
	}
}

// submit queues j. It fails with ErrClosed after Shutdown.
func (p *Pool) submit(j *job) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return ErrClosed
	}
	p.queue = append(p.queue, j)
	p.cond.Broadcast()
	return nil
}

// loop is one worker: it takes jobs until it is told to stop or the pool
// closes.
func (p *Pool) loop(w *worker) {
	defer p.running.Done()
	p.mu.Lock()
	for {
		for !w.stop && !p.closed && len(p.queue) == 0 {
			p.cond.Wait()
		}
		if w.stop || p.closed {
			break
		}
		j := p.queue[0]
		p.queue = p.queue[1:]
		ctx, cancel := context.WithCancelCause(j.ctx)
		w.busy, w.cancel = true, cancel
		p.mu.Unlock()

		j.exec(ctx)
		cancel(nil)

		p.mu.Lock()
		w.busy, w.cancel = false, nil
	}
	delete(p.live, w)
	if !w.stop {
		p.size--
	}
	p.mu.Unlock()
	p.events.emitWorker(p.hooks, onWorkerStop, WorkerEvent{Worker: w.id, Interrupted: w.interrupted})
}
