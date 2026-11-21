package loom_test

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brian-nunez/bkit/loom"
)

func fail(msg string) loom.Func[int] {
	return func(context.Context, *loom.Inputs) (int, error) { return 0, errors.New(msg) }
}

func TestTriggers(t *testing.T) {
	ok, bad, up, skip := loom.Succeeded, loom.Failed, loom.UpstreamFailed, loom.Skipped
	cases := []struct {
		name    string
		trigger loom.Trigger
		parents []loom.State
		want    bool
	}{
		{"AllSuccess yes", loom.AllSuccess, []loom.State{ok, ok}, true},
		{"AllSuccess no", loom.AllSuccess, []loom.State{ok, skip}, false},
		{"AllDone", loom.AllDone, []loom.State{bad, skip}, true},
		{"AllFailed yes", loom.AllFailed, []loom.State{bad, up}, true},
		{"AllFailed no", loom.AllFailed, []loom.State{bad, ok}, false},
		{"OneSuccess yes", loom.OneSuccess, []loom.State{bad, ok}, true},
		{"OneSuccess no", loom.OneSuccess, []loom.State{bad, skip}, false},
		{"OneFailed yes", loom.OneFailed, []loom.State{ok, up}, true},
		{"OneFailed no", loom.OneFailed, []loom.State{ok, skip}, false},
		{"NoneFailed yes", loom.NoneFailed, []loom.State{ok, skip}, true},
		{"NoneFailed no", loom.NoneFailed, []loom.State{ok, bad}, false},
	}
	for _, c := range cases {
		if got := c.trigger(c.parents); got != c.want {
			t.Errorf("%s: got %v", c.name, got)
		}
	}
}

func TestStateString(t *testing.T) {
	want := []string{"pending", "running", "retrying", "succeeded", "failed", "skipped", "upstream_failed", "canceled", "State(42)"}
	states := []loom.State{loom.Pending, loom.Running, loom.Retrying, loom.Succeeded, loom.Failed, loom.Skipped, loom.UpstreamFailed, loom.Canceled, 42}
	for i, s := range states {
		if s.String() != want[i] {
			t.Errorf("State(%d).String() = %q, want %q", int(s), s, want[i])
		}
	}
}

func TestTriggerDeclinesWithoutFailure(t *testing.T) {
	g := loom.New()
	a := loom.Add(g, "a", value(1))
	onFail := loom.Add(g, "onFail", value(2), loom.After(a), loom.WithTrigger(loom.AllFailed))
	res, err := g.Run(context.Background())
	if err != nil || res.State(onFail) != loom.Skipped || res.Err(onFail) != nil {
		t.Fatalf("state %v err %v run err %v", res.State(onFail), res.Err(onFail), err)
	}
}

func TestDefaultsAndOverrides(t *testing.T) {
	never := func(loom.Attempt) (time.Duration, bool) { return 0, false }
	tries := 0
	g := loom.New(loom.WithRetry(never), loom.WithTimeout(time.Hour), loom.WithTrigger(loom.AllDone))
	a := loom.Add(g, "a", fail("a"))
	retried := loom.Add(g, "retried", func(context.Context, *loom.Inputs) (int, error) {
		tries++
		if tries == 1 {
			return 0, errors.New("once")
		}
		return tries, nil
	}, loom.After(a), loom.WithRetry(loom.Backoff(1, 0, 0)), loom.WithTimeout(time.Minute), loom.WithTrigger(loom.OneFailed))
	strict := loom.Add(g, "strict", value(1), loom.After(a), loom.WithTrigger(loom.AllSuccess))

	res, _ := g.Run(context.Background())
	if loom.Get(res, retried) != 2 || res.State(a) != loom.Failed || res.State(strict) != loom.UpstreamFailed {
		t.Fatalf("retried=%v a=%v strict=%v", loom.Get(res, retried), res.State(a), res.State(strict))
	}
	wantErr(t, res.Err(strict), loom.ErrUpstream, `task "strict": upstream failed`)
}

func TestTimeout(t *testing.T) {
	g := loom.New()
	slow := loom.Add(g, "slow", func(ctx context.Context, _ *loom.Inputs) (int, error) {
		<-ctx.Done()
		return 0, context.Cause(ctx)
	}, loom.WithTimeout(time.Millisecond))
	res, err := g.Run(context.Background())
	wantErr(t, err, loom.ErrTimeout, `loom: task "slow" attempt 1: task timed out`)
	if res.State(slow) != loom.Failed {
		t.Fatalf("state %v", res.State(slow))
	}
}

func TestCallerCancel(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	g := loom.New()
	stuck := loom.Add(g, "stuck", func(context.Context, *loom.Inputs) (int, error) {
		close(started)
		<-release // ignores its context
		return 1, nil
	})
	after := loom.Add(g, "after", value(2), loom.After(stuck))

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-started
		cancel()
	}()
	res, err := g.Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if res.State(stuck) != loom.Canceled || res.State(after) != loom.Canceled {
		t.Fatalf("states %v %v", res.State(stuck), res.State(after))
	}
	if !errors.Is(res.Err(after), context.Canceled) {
		t.Fatalf("after err = %v", res.Err(after))
	}
}

func TestFailFastCancelsRunning(t *testing.T) {
	started := make(chan struct{})
	g := loom.New()
	loom.Add(g, "fails", func(context.Context, *loom.Inputs) (int, error) {
		<-started
		return 0, errors.New("boom")
	})
	waiter := loom.Add(g, "waiter", func(ctx context.Context, _ *loom.Inputs) (int, error) {
		close(started)
		<-ctx.Done()
		return 0, context.Cause(ctx)
	})
	pool, _ := loom.NewPool(loom.WithWorkers(2))
	defer pool.Close()

	res, err := g.Run(context.Background(), loom.FailFast(), loom.WithPool(pool))
	if res.State(waiter) != loom.Canceled {
		t.Fatalf("waiter %v", res.State(waiter))
	}
	var e *loom.Error
	if !errors.As(res.Err(waiter), &e) || e.Task != "waiter" {
		t.Fatalf("waiter err = %v", res.Err(waiter))
	}
	if !errors.As(err, &e) || e.Task != "fails" || errors.Is(err, context.Canceled) {
		t.Fatalf("run err = %v", err)
	}
}

func TestClosedPool(t *testing.T) {
	pool, _ := loom.NewPool(loom.WithWorkers(1))
	if err := pool.Close(); err != nil {
		t.Fatal(err)
	}
	g := loom.New(loom.WithPool(pool))
	a := loom.Add(g, "a", value(1))
	res, err := g.Run(context.Background())
	wantErr(t, err, loom.ErrClosed, "pool closed")
	wantErr(t, res.Err(a), loom.ErrClosed, `loom: task "a": pool closed`)
	if res.State(a) != loom.Canceled {
		t.Fatalf("state %v", res.State(a))
	}
}

func TestPoolValidationInRun(t *testing.T) {
	g := loom.New()
	loom.Add(g, "a", value(1))
	_, err := g.Run(context.Background(), loom.WithPool(&loom.Pool{}))
	wantErr(t, err, loom.ErrInvalidOption, "pool was not created with NewPool")
}

func TestNestedRun(t *testing.T) {
	pool, _ := loom.NewPool(loom.WithWorkers(2))
	defer pool.Close()
	inner := loom.New()
	loom.Add(inner, "inner", value(1))

	outer := loom.New(loom.WithPool(pool))
	loom.Add(outer, "outer", func(ctx context.Context, _ *loom.Inputs) (int, error) {
		if _, err := inner.Run(ctx, loom.WithPool(pool)); !errors.Is(err, loom.ErrNestedRun) {
			return 0, errors.New("nested run on the same pool was allowed")
		}
		_, err := inner.Run(ctx) // a private pool is fine
		return 1, err
	})
	if _, err := outer.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestResume(t *testing.T) {
	runs := map[string]int{}
	var mu sync.Mutex
	count := func(name string) {
		mu.Lock()
		defer mu.Unlock()
		runs[name]++
	}
	failOnce := true
	g := loom.New()
	a := loom.Add(g, "a", func(context.Context, *loom.Inputs) (int, error) { count("a"); return 1, nil })
	s := loom.Add(g, "s", func(context.Context, *loom.Inputs) (int, error) { count("s"); return 0, loom.ErrSkip })
	b := loom.Add(g, "b", func(ctx context.Context, in *loom.Inputs) (int, error) {
		count("b")
		if failOnce {
			failOnce = false
			return 0, errors.New("first time")
		}
		return loom.Get(in, a) + 1, nil
	}, loom.After(a))

	first, err := g.Run(context.Background())
	if err == nil || first.State(s) != loom.Skipped {
		t.Fatalf("first run: %v %v", err, first.State(s))
	}
	second, err := g.Run(context.Background(), loom.Resume(first))
	if err != nil || loom.Get(second, b) != 2 || second.State(s) != loom.Skipped {
		t.Fatalf("second run: %v %v %v", err, loom.Get(second, b), second.State(s))
	}
	if want := map[string]int{"a": 1, "s": 1, "b": 2}; !mapsEqual(runs, want) {
		t.Fatalf("runs = %v, want %v", runs, want)
	}
}

func mapsEqual(a, b map[string]int) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func TestHooksSeeEverything(t *testing.T) {
	var mu sync.Mutex
	var got []hookEntry
	record := func(source string) loom.Hooks {
		add := func(kind, task string, state loom.State) {
			mu.Lock()
			defer mu.Unlock()
			got = append(got, hookEntry{source, kind, task, state})
		}
		return loom.Hooks{
			OnRunStart:    func(_ context.Context, e loom.RunEvent) { add("runStart", e.Graph, 0) },
			OnRunFinish:   func(_ context.Context, e loom.RunEvent) { add("runFinish", e.Graph, 0) },
			OnTaskStart:   func(_ context.Context, e loom.TaskEvent) { add("taskStart", e.Task, e.State) },
			OnTaskRetry:   func(_ context.Context, e loom.TaskEvent) { add("taskRetry", e.Task, e.State) },
			OnTaskFinish:  func(_ context.Context, e loom.TaskEvent) { add("taskFinish", e.Task, e.State) },
			OnWorkerStart: func(loom.WorkerEvent) { add("workerStart", "", 0) },
			OnWorkerStop:  func(loom.WorkerEvent) { add("workerStop", "", 0) },
			OnResize:      func(loom.ResizeEvent) { add("resize", "", 0) },
		}
	}
	pool, err := loom.NewPool(loom.WithWorkers(1), loom.WithHooks(record("pool")))
	if err != nil {
		t.Fatal(err)
	}
	tries := 0
	g := loom.New(loom.WithName("g"), loom.WithHooks(record("graph")), loom.WithPool(pool))
	loom.Add(g, "flaky", func(context.Context, *loom.Inputs) (int, error) {
		tries++
		if tries == 1 {
			return 0, errors.New("again")
		}
		return 1, nil
	}, loom.WithRetry(func(loom.Attempt) (time.Duration, bool) { return -time.Second, true }))
	if _, err := g.Run(context.Background(), loom.WithHooks(record("run"))); err != nil {
		t.Fatal(err)
	}
	_ = pool.Grow(1)
	_ = pool.Shrink(1)
	_ = pool.Close()

	want := []hookEntry{
		{"pool", "workerStart", "", 0},
		{"graph", "runStart", "g", 0},
		{"run", "runStart", "g", 0},
		{"pool", "runStart", "g", 0},
		{"graph", "taskStart", "flaky", loom.Running},
		{"run", "taskStart", "flaky", loom.Running},
		{"pool", "taskStart", "flaky", loom.Running},
		{"graph", "taskRetry", "flaky", loom.Retrying},
		{"run", "taskRetry", "flaky", loom.Retrying},
		{"pool", "taskRetry", "flaky", loom.Retrying},
		{"graph", "taskStart", "flaky", loom.Running},
		{"run", "taskStart", "flaky", loom.Running},
		{"pool", "taskStart", "flaky", loom.Running},
		{"graph", "taskFinish", "flaky", loom.Succeeded},
		{"run", "taskFinish", "flaky", loom.Succeeded},
		{"pool", "taskFinish", "flaky", loom.Succeeded},
		{"graph", "runFinish", "g", 0},
		{"run", "runFinish", "g", 0},
		{"pool", "runFinish", "g", 0},
		{"pool", "workerStart", "", 0},
		{"pool", "resize", "", 0},
		{"pool", "resize", "", 0},
		{"pool", "workerStop", "", 0},
		{"pool", "workerStop", "", 0},
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n >= len(want) || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	// Pool and run events go through different dispatchers, so compare each
	// stream in order.
	for _, src := range []string{"pool", "graph", "run"} {
		var gotSrc, wantSrc []hookEntry
		for _, e := range got {
			if e.source == src {
				gotSrc = append(gotSrc, e)
			}
		}
		for _, e := range want {
			if e.source == src {
				wantSrc = append(wantSrc, e)
			}
		}
		if src == "pool" {
			gotSrc, wantSrc = sortedByKind(gotSrc), sortedByKind(wantSrc)
		}
		if !slices.Equal(gotSrc, wantSrc) {
			t.Errorf("%s events:\n got %v\nwant %v", src, gotSrc, wantSrc)
		}
	}
}

type hookEntry struct {
	source, kind, task string
	state              loom.State
}

// sortedByKind orders pool-hook events, which interleave worker events and
// run events from two dispatchers, into a comparable order.
func sortedByKind(es []hookEntry) []hookEntry {
	out := slices.Clone(es)
	slices.SortStableFunc(out, func(a, b hookEntry) int { return cmp.Compare(a.kind, b.kind) })
	return out
}

// A task queued before FailFast stops the run never starts: it ends
// Canceled when a worker picks it up.
func TestFailFastQueuedTaskNeverStarts(t *testing.T) {
	pool, _ := loom.NewPool(loom.WithWorkers(1))
	defer pool.Close()
	stopped := make(chan struct{})
	g := loom.New(loom.WithPool(pool), loom.FailFast(), loom.WithHooks(loom.Hooks{
		OnTaskFinish: func(_ context.Context, e loom.TaskEvent) {
			if e.Task == "fails" {
				close(stopped)
			}
		},
	}))
	loom.Add(g, "fails", func(context.Context, *loom.Inputs) (int, error) {
		_ = pool.Shrink(1) // this worker leaves after the task, so "queued" waits
		return 0, errors.New("boom")
	})
	ran := false
	queued := loom.Add(g, "queued", func(context.Context, *loom.Inputs) (int, error) {
		ran = true
		return 1, nil
	})
	go func() {
		<-stopped
		_ = pool.Grow(1)
	}()
	res, _ := g.Run(context.Background())
	if ran || res.State(queued) != loom.Canceled {
		t.Fatalf("queued ran=%v state=%v", ran, res.State(queued))
	}
	var e *loom.Error
	if !errors.As(res.Err(queued), &e) || e.Task != "queued" || !strings.Contains(e.Error(), `task "fails"`) {
		t.Fatalf("queued err = %v", res.Err(queued))
	}
}

// A task waiting out its retry delay ends Canceled as soon as FailFast stops
// the run, without waiting for the delay.
func TestFailFastCancelsPendingRetry(t *testing.T) {
	retrying := make(chan struct{})
	g := loom.New(loom.FailFast(), loom.WithHooks(loom.Hooks{
		OnTaskRetry: func(context.Context, loom.TaskEvent) { close(retrying) },
	}))
	slow := loom.Add(g, "slow", fail("again"), loom.WithRetry(func(loom.Attempt) (time.Duration, bool) {
		return time.Hour, true
	}))
	loom.Add(g, "fails", func(context.Context, *loom.Inputs) (int, error) {
		<-retrying
		return 0, errors.New("boom")
	})
	start := time.Now()
	res, _ := g.Run(context.Background())
	if res.State(slow) != loom.Canceled || time.Since(start) > time.Minute {
		t.Fatalf("slow %v after %v", res.State(slow), time.Since(start))
	}
}
