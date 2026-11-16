package loom_test

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brian-nunez/bkit/loom"
)

// selfError wraps itself, so a naive errors.Is walk never ends.
type selfError struct{}

func (e *selfError) Error() string { return "self" }
func (e *selfError) Unwrap() error { return e }

// panicError panics from every method.
type panicError struct{}

func (panicError) Error() string   { panic("Error") }
func (panicError) Unwrap() []error { panic("Unwrap") }

// nilError unwraps to nothing.
type nilError struct{}

func (nilError) Error() string { return "nil unwrap" }
func (nilError) Unwrap() error { return nil }

type nilJoin struct{}

func (nilJoin) Error() string   { return "nil join" }
func (nilJoin) Unwrap() []error { return []error{nil, loom.ErrSkip} }

// Every way a task can misbehave becomes a task state. Nothing escapes.
func TestHostileTasks(t *testing.T) {
	g := loom.New()
	panics := loom.Add(g, "panics", func(context.Context, *loom.Inputs) (int, error) { panic("boom") })
	panicsErr := loom.Add(g, "panicsErr", func(context.Context, *loom.Inputs) (int, error) { panic(context.Canceled) })
	exits := loom.Add(g, "exits", func(context.Context, *loom.Inputs) (int, error) {
		runtime.Goexit()
		return 0, nil
	})
	loops := loom.Add(g, "loops", func(context.Context, *loom.Inputs) (int, error) { return 0, &selfError{} })
	unprintable := loom.Add(g, "unprintable", func(context.Context, *loom.Inputs) (int, error) { return 0, panicError{} })
	nils := loom.Add(g, "nils", func(context.Context, *loom.Inputs) (int, error) { return 0, nilError{} })
	joined := loom.Add(g, "joined", func(context.Context, *loom.Inputs) (int, error) { return 0, nilJoin{} })

	res, err := g.Run(context.Background())
	if err == nil {
		t.Fatal("run succeeded")
	}
	for task, want := range map[*loom.Task[int]]string{
		panics:      `loom: task "panics" attempt 1: task panicked: boom`,
		panicsErr:   `loom: task "panicsErr" attempt 1: task panicked: context canceled`,
		exits:       `loom: task "exits" attempt 1: task panicked: called runtime.Goexit`,
		loops:       `loom: task "loops" attempt 1: self`,
		unprintable: `loom: task "unprintable" attempt 1: unprintable error`,
		nils:        `loom: task "nils" attempt 1: nil unwrap`,
	} {
		if res.State(task) != loom.Failed || res.Err(task).Error() != want {
			t.Errorf("%s: %v %q", task.Name(), res.State(task), res.Err(task))
		}
	}
	if !errors.Is(res.Err(panics), loom.ErrPanic) || !errors.Is(res.Err(panicsErr), context.Canceled) {
		t.Error("panic errors do not match their kind and value")
	}
	if res.State(joined) != loom.Skipped {
		t.Errorf("ErrSkip inside a join with nil entries: %v", res.State(joined))
	}
}

func TestHostileTriggerAndPolicy(t *testing.T) {
	g := loom.New()
	a := loom.Add(g, "a", value(1))
	panics := loom.Add(g, "panics", value(2), loom.After(a), loom.WithTrigger(func([]loom.State) bool { panic("trigger") }))
	exits := loom.Add(g, "exits", value(3), loom.After(a), loom.WithTrigger(func([]loom.State) bool {
		runtime.Goexit()
		return true
	}))
	policyPanics := loom.Add(g, "policy", fail("x"), loom.WithRetry(func(loom.Attempt) (time.Duration, bool) { panic("policy") }))

	res, _ := g.Run(context.Background())
	wantErr(t, res.Err(panics), loom.ErrPanic, `loom: task "panics": task panicked: trigger`)
	wantErr(t, res.Err(exits), loom.ErrPanic, "called runtime.Goexit")
	if res.State(policyPanics) != loom.Failed || !strings.Contains(res.Err(policyPanics).Error(), "attempt 1: x") {
		t.Errorf("policy panic: %v %v", res.State(policyPanics), res.Err(policyPanics))
	}
}

// Hooks that panic, exit, or block never stall tasks, and later hooks still
// run.
func TestHostileHooks(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	var mu sync.Mutex
	var finished []string
	delivered := make(chan struct{})
	hostile := loom.Hooks{
		OnRunStart:  func(context.Context, loom.RunEvent) { panic("hook") },
		OnTaskStart: func(context.Context, loom.TaskEvent) { runtime.Goexit() },
	}
	recorder := loom.Hooks{
		OnTaskFinish: func(_ context.Context, e loom.TaskEvent) {
			mu.Lock()
			defer mu.Unlock()
			finished = append(finished, e.Task)
		},
		OnRunFinish: func(context.Context, loom.RunEvent) { close(delivered) },
	}
	g := loom.New(loom.WithHooks(hostile), loom.WithHooks(recorder))
	a := loom.Add(g, "a", value(1))
	res, err := g.Run(context.Background())
	if err != nil || loom.Get(res, a) != 1 {
		t.Fatalf("%v %v", loom.Get(res, a), err)
	}
	<-delivered
	mu.Lock()
	defer mu.Unlock()
	if len(finished) != 1 || finished[0] != "a" {
		t.Fatalf("finished = %v", finished)
	}

	// A hook that never returns delays only later hooks, never the run.
	stuck := loom.New(loom.WithHooks(loom.Hooks{OnRunStart: func(context.Context, loom.RunEvent) { <-block }}))
	b := loom.Add(stuck, "b", value(2))
	res, err = stuck.Run(context.Background())
	if err != nil || loom.Get(res, b) != 2 {
		t.Fatalf("%v %v", loom.Get(res, b), err)
	}
}

func TestErrorFormat(t *testing.T) {
	cases := map[string]*loom.Error{
		`loom: task "t"`:                       {Task: "t"},
		`loom: task "t" attempt 2`:             {Task: "t", Attempt: 2},
		`loom: task "t": upstream failed`:      {Task: "t", Kind: loom.ErrUpstream},
		`loom: task "t" attempt 1: boom`:       {Task: "t", Attempt: 1, Err: errors.New("boom")},
		`loom: task "t": task timed out: boom`: {Task: "t", Kind: loom.ErrTimeout, Err: errors.New("boom")},
	}
	for want, e := range cases {
		if e.Error() != want {
			t.Errorf("got %q, want %q", e.Error(), want)
		}
	}
	if len((&loom.Error{Task: "t"}).Unwrap()) != 0 {
		t.Error("an empty Error unwraps to something")
	}
}

func TestBackoff(t *testing.T) {
	p := loom.Backoff(4, time.Second, 5*time.Second)
	for n, want := range map[int]time.Duration{1: time.Second, 2: 2 * time.Second, 3: 4 * time.Second, 4: 5 * time.Second} {
		if d, ok := p(loom.Attempt{N: n}); !ok || d != want {
			t.Errorf("attempt %d: %v %v, want %v", n, d, ok, want)
		}
	}
	if _, ok := p(loom.Attempt{N: 5}); ok {
		t.Error("retried past the limit")
	}
}
