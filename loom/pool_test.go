package loom_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/brian-nunez/bkit/loom"
)

func TestNewPoolOptions(t *testing.T) {
	_, err := loom.NewPool(nil, loom.WithWorkers(-1))
	wantErr(t, err, loom.ErrInvalidOption, "nil pool option")
	wantErr(t, err, loom.ErrInvalidOption, "WithWorkers needs zero or more workers")

	p, err := loom.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if p.Size() < 1 {
		t.Fatalf("default size %d", p.Size())
	}
}

func TestUnusablePool(t *testing.T) {
	for _, p := range []*loom.Pool{nil, {}} {
		if p.Size() != 0 {
			t.Error("unusable pool has workers")
		}
		for _, err := range []error{p.Grow(1), p.Shrink(1), p.Shutdown(context.Background()), p.Close()} {
			wantErr(t, err, loom.ErrInvalidOption, "pool was not created with NewPool")
		}
	}
}

func TestPoolSizes(t *testing.T) {
	p, _ := loom.NewPool(loom.WithWorkers(2))
	if !errors.Is(p.Grow(0), loom.ErrInvalidSize) || !errors.Is(p.Shrink(0), loom.ErrInvalidSize) || !errors.Is(p.Shrink(3), loom.ErrInvalidSize) {
		t.Error("invalid sizes were accepted")
	}
	if err := p.Shrink(2, nil); err != nil || p.Size() != 0 {
		t.Fatalf("shrink to zero: %v size %d", err, p.Size())
	}
	var nilCtx context.Context
	wantErr(t, p.Shutdown(nilCtx), loom.ErrInvalidOption, "nil context")
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(p.Grow(1), loom.ErrClosed) || !errors.Is(p.Shrink(1), loom.ErrClosed) {
		t.Error("a closed pool accepted Grow or Shrink")
	}
	if err := p.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// A paused pool queues work until Grow.
func TestPausedPool(t *testing.T) {
	p, _ := loom.NewPool(loom.WithWorkers(0))
	defer p.Close()
	g := loom.New(loom.WithPool(p))
	a := loom.Add(g, "a", value(1))
	go func() {
		time.Sleep(10 * time.Millisecond)
		_ = p.Grow(1)
	}()
	res, err := g.Run(context.Background())
	if err != nil || loom.Get(res, a) != 1 {
		t.Fatalf("%v %v", loom.Get(res, a), err)
	}
}

// Graceful Shrink lets a busy worker finish its task, and idle workers go
// first.
func TestShrinkGraceful(t *testing.T) {
	stops := make(chan loom.WorkerEvent, 4)
	p, _ := loom.NewPool(loom.WithWorkers(3), loom.WithHooks(loom.Hooks{
		OnWorkerStop: func(e loom.WorkerEvent) { stops <- e },
	}))
	defer p.Close()

	started, release := make(chan struct{}), make(chan struct{})
	g := loom.New(loom.WithPool(p))
	busy := loom.Add(g, "busy", func(ctx context.Context, _ *loom.Inputs) (int, error) {
		close(started)
		<-release
		return 1, ctx.Err()
	})
	done := make(chan *loom.Result)
	go func() {
		res, _ := g.Run(context.Background())
		done <- res
	}()
	<-started
	if err := p.Shrink(3); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if e := <-stops; e.Interrupted {
			t.Error("an idle worker reported an interrupt")
		}
	}
	close(release)
	res := <-done
	if loom.Get(res, busy) != 1 {
		t.Fatalf("busy task did not finish: %v", res.Err(busy))
	}
	if e := <-stops; e.Interrupted {
		t.Error("the busy worker reported an interrupt")
	}
}

// When the Shutdown deadline passes, running tasks are interrupted and
// Shutdown returns without waiting for a task that ignores its context.
func TestShutdownDeadline(t *testing.T) {
	stops := make(chan loom.WorkerEvent, 2)
	p, _ := loom.NewPool(loom.WithWorkers(2), loom.WithHooks(loom.Hooks{
		OnWorkerStop: func(e loom.WorkerEvent) { stops <- e },
	}))
	started, release := make(chan struct{}), make(chan struct{})
	causes := make(chan error, 1)
	g := loom.New(loom.WithPool(p))
	loom.Add(g, "stubborn", func(ctx context.Context, _ *loom.Inputs) (int, error) {
		close(started)
		<-ctx.Done()
		causes <- context.Cause(ctx)
		<-release
		return 0, nil
	})
	go func() { _, _ = g.Run(context.Background()) }()
	<-started

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := p.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown = %v", err)
	}
	if cause := <-causes; !errors.Is(cause, loom.ErrInterrupted) {
		t.Fatalf("cause = %v", cause)
	}
	close(release)
	interrupted := 0
	for range 2 {
		if (<-stops).Interrupted {
			interrupted++
		}
	}
	if interrupted != 1 {
		t.Fatalf("%d workers reported an interrupt, want 1", interrupted)
	}
}
