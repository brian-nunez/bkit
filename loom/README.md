# loom

Graphs of typed Go functions, run in parallel on a worker pool you can grow
and shrink while it runs.

A task is a plain function. loom starts each task as soon as its parents are
done, passes their values in with their real types, and reports what
happened. Retries, timeouts, and failure handling are options you set. loom
never logs. Hooks report every event. It only uses the standard library.

## Install

```shell
go get github.com/brian-nunez/bkit/loom
```

```go
import "github.com/brian-nunez/bkit/loom"
```

## Usage

`Add` puts a function in a graph and returns a typed handle. `After` declares
the tasks it depends on, and `Get` reads their values:

```go
g := loom.New()
users := loom.Add(g, "users", fetchUsers)   // func(ctx, *loom.Inputs) ([]User, error)
orders := loom.Add(g, "orders", fetchOrders) // runs in parallel with users
report := loom.Add(g, "report", func(ctx context.Context, in *loom.Inputs) (Report, error) {
	return build(loom.Get(in, users), loom.Get(in, orders)), nil
}, loom.After(users, orders))

res, err := g.Run(ctx)
if err != nil {
	return err
}
fmt.Println(loom.Get(res, report)) // a Report
```

Every task has the same shape, `func(ctx context.Context, in *loom.Inputs)
(T, error)`. Tasks without parents ignore `in`. A task can only depend on
tasks added before it, so a graph never has a cycle. Graph mistakes, such as
a duplicate name or a parent from another graph, fail `Validate` and `Run`
before any task starts.

`Run` returns a `*loom.Result` even when it fails, so every task's state is
available:

```go
res, err := g.Run(ctx)
fmt.Println(res.State(report), res.Err(report))
```

A graph can run any number of times, concurrently. Inputs reach tasks
through closures, so a service builds one graph per job. Building a graph
does no work.

### Failure handling

Failure handling follows Apache Airflow, and every part of it is an option.

| Task state | Meaning |
| --- | --- |
| `Succeeded` | Returned a nil error |
| `Failed` | Returned an error, panicked, or ran out of retries |
| `Skipped` | Returned `loom.ErrSkip`, or its trigger rule declined without a failed parent |
| `UpstreamFailed` | Its trigger rule declined because a parent failed |
| `Canceled` | The run's context ended, `FailFast` stopped the run, or the pool closed |

A trigger rule decides whether a task with parents runs once they are done.
The default is `AllSuccess`. `AllDone` runs cleanup whatever happened, and
`Lookup` tells it which parents produced a value:

```go
loom.Add(g, "cleanup", func(ctx context.Context, in *loom.Inputs) (struct{}, error) {
	if files, ok := loom.Lookup(in, download); ok {
		removeAll(files.Paths)
	}
	return struct{}{}, nil
}, loom.After(download, upload), loom.WithTrigger(loom.AllDone))
```

| Trigger | Runs the task when |
| --- | --- |
| `AllSuccess` | Every parent succeeded (default) |
| `AllDone` | Every parent is done, whatever its state |
| `AllFailed` | Every parent failed or was upstream-failed |
| `OneSuccess` | At least one parent succeeded |
| `OneFailed` | At least one parent failed or was upstream-failed |
| `NoneFailed` | No parent failed or was upstream-failed |

Any `func(parents []loom.State) bool` is a trigger too.

Tasks are retried only by a policy you write. The policy sees each failed
attempt and returns a delay and whether to retry. `loom.Backoff` is a
ready-made policy you can call from your own:

```go
policy := func(a loom.Attempt) (time.Duration, bool) {
	switch {
	case errors.Is(a.Err, loom.ErrInterrupted):
		return 0, true // the worker was scaled down, so run it again now
	case errors.Is(a.Err, errBadInput):
		return 0, false
	default:
		return loom.Backoff(3, time.Second, 30*time.Second)(a)
	}
}
g := loom.New(loom.WithRetry(policy), loom.WithTimeout(time.Minute))
```

Settings on `New` are defaults for every task. The same options on `Add`
override them for one task, and `loom.WithRetry(nil)` turns retries off.

`loom.FailFast()` stops a run at its first failure. `loom.Resume(prev)`
reruns only the tasks that did not succeed or skip in an earlier result, and
reuses the values of those that did:

```go
res, err := g.Run(ctx)
if err != nil {
	res, err = g.Run(ctx, loom.Resume(res))
}
```

| Option | On | Effect | Default |
| --- | --- | --- | --- |
| `After(tasks...)` | `Add` | The task's parents | No parents |
| `WithRetry(policy)` | `New`, `Add` | Retry policy | No retries |
| `WithTimeout(d)` | `New`, `Add` | Limit for each attempt | No limit |
| `WithTrigger(t)` | `New`, `Add` | Trigger rule | `AllSuccess` |
| `WithName(name)` | `New` | Name carried by every event | Empty |
| `WithPool(p)` | `New`, `Run` | Pool that runs the tasks | A private pool per run |
| `FailFast()` | `New`, `Run` | Stop at the first failure | Off |
| `WithHooks(h)` | `New`, `Run`, `NewPool` | Event callbacks | None |
| `Resume(prev)` | `Run` | Rerun what did not succeed in `prev` | Off |
| `WithWorkers(n)` | `NewPool` | Starting worker count | `runtime.GOMAXPROCS(0)` |
| `Interrupt()` | `Shrink` | Cancel the removed workers' tasks | Off |

### Pools

A pool is a set of workers that any number of graphs and runs share. It
changes size while it runs:

```go
pool, err := loom.NewPool(loom.WithWorkers(4))
if err != nil {
	return err
}
defer pool.Close()

g := loom.New(loom.WithPool(pool))

pool.Grow(4)                     // 8 workers
pool.Shrink(2)                   // 2 workers leave after their current task
pool.Shrink(2, loom.Interrupt()) // 2 workers cancel their tasks and leave
```

An interrupted task's context is canceled with the cause
`loom.ErrInterrupted`. Its retry policy decides whether it runs again on
another worker. `Shrink` to zero pauses the pool, and queued tasks wait for
`Grow`.

`Shutdown(ctx)` stops the pool. Queued tasks end `Canceled` with
`ErrClosed`, and running tasks get until `ctx` ends to finish. Then their
contexts are canceled with `ErrInterrupted` and `Shutdown` returns. `Close`
is `Shutdown` with no deadline.

A run inside a task that uses the task's own pool fails with `ErrNestedRun`,
because the outer task holds a worker while it waits. Give the inner run its
own pool, or none.

### Hooks

Hooks are callbacks for events. loom calls them in order on a separate
goroutine, so a slow hook never delays a task:

```go
g := loom.New(loom.WithName("ingest"), loom.WithHooks(loom.Hooks{
	OnTaskFinish: func(ctx context.Context, e loom.TaskEvent) {
		slog.InfoContext(ctx, "task", "graph", e.Graph, "task", e.Task,
			"state", e.State, "attempt", e.Attempt, "err", e.Err)
	},
}))
```

| Hook | Called when |
| --- | --- |
| `OnRunStart`, `OnRunFinish` | A run starts or returns |
| `OnTaskStart` | An attempt starts on a worker |
| `OnTaskRetry` | A failed attempt will run again |
| `OnTaskFinish` | A task reaches a final state |
| `OnWorkerStart`, `OnWorkerStop` | A worker joins or leaves a pool |
| `OnResize` | `Grow` or `Shrink` changes a pool's size |

Hooks on a pool receive its worker events plus the run and task events of
every graph on the pool. A hook can run after `Run` returns, and the context
it receives can already be canceled.

## Untrusted task code

loom treats every function you pass in as untrusted. A task, trigger, retry
policy, or hook that panics or calls `runtime.Goexit` is recovered on its own
goroutine. A failing task becomes `Failed` with `ErrPanic`, and a failing
hook is dropped. Error chains are walked with a depth limit, so an error that
wraps itself or panics in its methods cannot hang loom.

loom cancels a task's context on timeout, interrupt, or cancellation, then
waits for the task to return. Go cannot stop a goroutine, so a task that
ignores its context keeps its worker until it returns. `Run` and `Shutdown`
still return when their own contexts end. A goroutine a task starts on its
own is outside loom, and a panic there still ends the process.

## Errors

A task that does not succeed reports a `*loom.Error` with the task's name,
the attempt, a `Kind`, and the task's own error. `Run` joins the errors of
every failed task. Match them with `errors.Is` and `errors.As`:

```go
res, err := g.Run(ctx)
var e *loom.Error
if errors.As(err, &e) {
	log.Printf("task %s failed on attempt %d: %v", e.Task, e.Attempt, e.Err)
}
if errors.Is(err, loom.ErrTimeout) {
	// at least one task timed out
}
```

| Sentinel | Meaning |
| --- | --- |
| `ErrInvalidGraph` | A nil graph or function, an empty or duplicate name, a foreign parent, or an `Add` after `Run` |
| `ErrInvalidOption` | An out-of-range option, a nil option or context, a foreign `Resume` result, or a pool not made by `NewPool` |
| `ErrSkip` | Returned by a task to mark it `Skipped` |
| `ErrUpstream` | Kind of a task whose trigger rule saw a failed parent |
| `ErrTimeout` | Kind of an attempt that failed after its timeout |
| `ErrPanic` | Kind of an attempt or trigger that panicked or called `runtime.Goexit` |
| `ErrInterrupted` | Kind of an attempt that failed after its worker was interrupted |
| `ErrClosed` | A pool call after `Shutdown`, or a task that could not start because its pool closed |
| `ErrInvalidSize` | A `Grow` or `Shrink` count below one, or a `Shrink` larger than the pool |
| `ErrNestedRun` | A run inside a task on the same pool |

When the run's context ends, the error also matches `context.Cause(ctx)`.

## Example service

[`examples/ingest`](examples/ingest) is a complete service. It runs one
graph per job on a shared pool, scales the pool with load, sheds workers with
`Interrupt`, retries with its own policy, and resumes failed jobs:

```shell
cd loom/examples/ingest
go run .
```

## Development

From the repository root:

```shell
make -C loom check
```

`check` runs the format check, lint, vet, race-enabled tests, and
`coverage-check`, which requires 100% statement coverage.

## Releases

Each release is tagged `loom/vX.Y.Z`:

```shell
go get github.com/brian-nunez/bkit/loom@vX.Y.Z
```
