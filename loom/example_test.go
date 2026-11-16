package loom_test

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/brian-nunez/bkit/loom"
)

type User struct{ ID int }

type Order struct{ ID, UserID int }

type Report struct{ Users, Orders int }

func fetchUsers(context.Context, *loom.Inputs) ([]User, error) {
	return []User{{1}, {2}}, nil
}

func fetchOrders(context.Context, *loom.Inputs) ([]Order, error) {
	return []Order{{1, 1}, {2, 1}, {3, 2}}, nil
}

// Typed tasks, declared dependencies, and one Run. users and orders run in
// parallel, and report starts when both are done.
func Example() {
	g := loom.New()
	users := loom.Add(g, "users", fetchUsers)
	orders := loom.Add(g, "orders", fetchOrders)
	report := loom.Add(g, "report", func(ctx context.Context, in *loom.Inputs) (Report, error) {
		return Report{len(loom.Get(in, users)), len(loom.Get(in, orders))}, nil
	}, loom.After(users, orders))

	res, err := g.Run(context.Background())
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Printf("%+v\n", loom.Get(res, report))
	// Output: {Users:2 Orders:3}
}

// A failed task fails its children under the default AllSuccess trigger,
// independent tasks still run, and the error names the task.
func Example_taskFails() {
	g := loom.New()
	users := loom.Add(g, "users", func(context.Context, *loom.Inputs) ([]User, error) {
		return nil, errors.New("connection refused")
	})
	count := loom.Add(g, "count", func(ctx context.Context, in *loom.Inputs) (int, error) {
		return len(loom.Get(in, users)), nil
	}, loom.After(users))
	orders := loom.Add(g, "orders", fetchOrders)

	res, err := g.Run(context.Background())
	fmt.Println(err)
	var e *loom.Error
	if errors.As(err, &e) {
		fmt.Println(e.Task, e.Attempt)
	}
	fmt.Println(res.State(users), res.State(count), res.State(orders))
	// Output:
	// loom: task "users" attempt 1: connection refused
	// users 1
	// failed upstream_failed succeeded
}

// Graph mistakes are reported before any task runs.
func Example_invalidGraph() {
	g := loom.New()
	loom.Add(g, "users", fetchUsers)
	loom.Add(g, "users", fetchUsers)

	err := g.Validate()
	fmt.Println(errors.Is(err, loom.ErrInvalidGraph))
	fmt.Println(err)
	// Output:
	// true
	// loom: invalid graph: task "users": duplicate name
}

// The application owns retries. This policy retries interrupted work right
// away, never retries bad input, and backs off on everything else.
func ExampleRetryPolicy() {
	errBadInput := errors.New("bad input")
	policy := func(a loom.Attempt) (time.Duration, bool) {
		switch {
		case errors.Is(a.Err, loom.ErrInterrupted):
			return 0, true
		case errors.Is(a.Err, errBadInput):
			return 0, false
		default:
			return loom.Backoff(3, time.Millisecond, 10*time.Millisecond)(a)
		}
	}

	calls := 0
	g := loom.New(loom.WithRetry(policy))
	flaky := loom.Add(g, "flaky", func(context.Context, *loom.Inputs) (string, error) {
		calls++
		if calls < 3 {
			return "", errors.New("temporary")
		}
		return "ok", nil
	})

	res, err := g.Run(context.Background())
	fmt.Println(loom.Get(res, flaky), calls, err)
	// Output: ok 3 <nil>
}

// ErrSkip skips a task, and AllDone runs cleanup whatever happened. Lookup
// tells cleanup whether a parent produced a value.
func ExampleWithTrigger() {
	g := loom.New()
	upload := loom.Add(g, "upload", func(context.Context, *loom.Inputs) (string, error) {
		return "", errors.New("bucket unavailable")
	})
	notify := loom.Add(g, "notify", func(context.Context, *loom.Inputs) (struct{}, error) {
		return struct{}{}, loom.ErrSkip
	})
	cleanup := loom.Add(g, "cleanup", func(ctx context.Context, in *loom.Inputs) (string, error) {
		if _, ok := loom.Lookup(in, upload); !ok {
			return "nothing uploaded", nil
		}
		return "removed upload", nil
	}, loom.After(upload, notify), loom.WithTrigger(loom.AllDone))

	res, _ := g.Run(context.Background())
	fmt.Println(res.State(upload), res.State(notify), loom.Get(res, cleanup))
	// Output: failed skipped nothing uploaded
}

// Resume reruns only what did not succeed, reusing earlier values.
func ExampleResume() {
	downloads, uploads := 0, 0
	g := loom.New()
	download := loom.Add(g, "download", func(context.Context, *loom.Inputs) (string, error) {
		downloads++
		return "/tmp/video.mp4", nil
	})
	loom.Add(g, "upload", func(ctx context.Context, in *loom.Inputs) (string, error) {
		uploads++
		if uploads == 1 {
			return "", errors.New("expired credentials")
		}
		return "uploaded " + loom.Get(in, download), nil
	}, loom.After(download))

	res, err := g.Run(context.Background())
	fmt.Println(err)
	_, err = g.Run(context.Background(), loom.Resume(res))
	fmt.Println(err, downloads, uploads)
	// Output:
	// loom: task "upload" attempt 1: expired credentials
	// <nil> 1 2
}

// FailFast stops the run at the first failure: tasks that have not started
// end Canceled.
func ExampleFailFast() {
	g := loom.New(loom.FailFast())
	check := loom.Add(g, "check", func(context.Context, *loom.Inputs) (int, error) {
		return 0, errors.New("disk full")
	})
	write := loom.Add(g, "write", func(context.Context, *loom.Inputs) (int, error) {
		return 1, nil
	}, loom.After(check), loom.WithTrigger(loom.AllDone))

	res, _ := g.Run(context.Background())
	fmt.Println(res.State(check), res.State(write))
	// Output: failed canceled
}

// loom never logs. Hooks receive events in order on their own goroutine.
func ExampleHooks() {
	finished := make(chan struct{})
	g := loom.New(loom.WithName("etl"), loom.WithHooks(loom.Hooks{
		OnTaskFinish: func(ctx context.Context, e loom.TaskEvent) {
			fmt.Println(e.Graph, e.Task, e.State, e.Attempt)
		},
		OnRunFinish: func(context.Context, loom.RunEvent) { close(finished) },
	}))
	loom.Add(g, "users", fetchUsers)

	_, _ = g.Run(context.Background())
	<-finished
	// Output: etl users succeeded 1
}

// One pool, shared by any number of runs, resized while they run. Shrink
// with Interrupt cancels the removed workers' tasks, and the retry policy
// runs them again on the workers that remain.
func ExamplePool() {
	pool, err := loom.NewPool(loom.WithWorkers(2))
	if err != nil {
		fmt.Println(err)
		return
	}
	defer pool.Close()

	_ = pool.Grow(2)
	fmt.Println(pool.Size())

	started := make(chan struct{})
	attempts := 0
	g := loom.New(loom.WithPool(pool))
	encode := loom.Add(g, "encode", func(ctx context.Context, _ *loom.Inputs) (int, error) {
		attempts++
		if attempts == 1 {
			close(started)
			<-ctx.Done()
			return 0, context.Cause(ctx)
		}
		return attempts, nil
	}, loom.WithRetry(func(a loom.Attempt) (time.Duration, bool) {
		return 0, errors.Is(a.Err, loom.ErrInterrupted)
	}))

	go func() {
		<-started
		_ = pool.Shrink(4, loom.Interrupt())
		_ = pool.Grow(1)
	}()
	res, err := g.Run(context.Background())
	fmt.Println(loom.Get(res, encode), err, pool.Size())
	// Output:
	// 4
	// 2 <nil> 1
}

// Build graphs in functions that take handles. Tests then feed fixed
// parents, with no infrastructure.
func Example_testing() {
	countUsers := func(g *loom.Graph, users *loom.Task[[]User]) *loom.Task[int] {
		return loom.Add(g, "count", func(ctx context.Context, in *loom.Inputs) (int, error) {
			return len(loom.Get(in, users)), nil
		}, loom.After(users))
	}

	g := loom.New()
	users := loom.Add(g, "users", func(context.Context, *loom.Inputs) ([]User, error) {
		return []User{{1}, {2}, {3}}, nil
	})
	count := countUsers(g, users)

	res, err := g.Run(context.Background())
	fmt.Println(loom.Get(res, count), err)
	// Output: 3 <nil>
}
