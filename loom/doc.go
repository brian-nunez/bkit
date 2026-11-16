// Package loom runs graphs of typed Go functions on an elastic worker pool.
//
// A [Graph] holds tasks. [Add] puts a plain function in it and returns a
// typed [Task] handle, [After] declares the tasks it depends on, and [Get]
// reads a parent's value inside a task or any task's value from the
// [Result] after a run. Each task starts as soon as its parents are done and
// its [Trigger] rule agrees, so independent tasks run in parallel:
//
//	g := loom.New()
//	users := loom.Add(g, "users", fetchUsers)
//	orders := loom.Add(g, "orders", fetchOrders)
//	report := loom.Add(g, "report", func(ctx context.Context, in *loom.Inputs) (Report, error) {
//		return build(loom.Get(in, users), loom.Get(in, orders)), nil
//	}, loom.After(users, orders))
//
//	res, err := g.Run(ctx)
//	fmt.Println(loom.Get(res, report))
//
// A [Pool] is a set of workers that any number of graphs and runs can share.
// [Pool.Grow] and [Pool.Shrink] change its size while it runs, and
// [Interrupt] makes Shrink cancel the removed workers' tasks instead of
// letting them finish.
//
// The package owns scheduling, task state, and its own integrity. It treats
// every function the application passes in (tasks, triggers, retry
// policies, hooks) as untrusted: a panic or runtime.Goexit in one is
// recovered on its own goroutine, and error chains are walked with a depth
// limit, so application code cannot crash or wedge loom. A goroutine that a
// task starts on its own is outside loom, and a panic there still ends the
// process.
//
// The application owns every policy. Tasks are retried only by a
// [RetryPolicy] it sets, an interrupted task's fate is decided by that
// policy, and loom never logs: [Hooks] report events, delivered in order on
// a separate goroutine. loom cancels a task's context on timeout,
// interrupt, or cancellation, then waits for the task to return; a task
// that ignores its context keeps its worker until it does.
//
// The zero value of Graph is an empty, usable graph. Create a Pool with
// [NewPool]; its zero value is not usable. Graph, Pool, and Result are safe
// for concurrent use.
package loom
