---
status: "accepted"
date: 2026-11-15
decision-makers: Brian Nunez
---

# Run loom tasks as untrusted code, with every task policy owned by the caller

## Context and Problem Statement

loom runs graphs of plain Go functions on an elastic worker pool inside one
process. The functions belong to the application, and loom cannot inspect,
preempt, or kill them: a goroutine runs until it returns. A function can
panic, call `runtime.Goexit`, ignore its context, block forever, or return
an error whose own methods panic or loop. Hooks and policy callbacks the
application registers can do the same. How does loom stay correct around
code it does not control, and who decides what happens to a task when it
fails, times out, or loses its worker?

## Decision Drivers

* loom itself never panics, deadlocks, or corrupts its state, whatever the
  application's functions do.
* A task's outcome is what the task returns. loom never rewrites it.
* Retries, interrupts, and failure handling differ per application, so the
  application decides them with options it passes in.
* Observability is the application's: loom never logs.
* Core module stays standard library only.

## Considered Options

* Isolate user code and hand every decision to caller-supplied policy
* Built-in policies: a fixed retry count, automatic requeue of interrupted
  work, built-in logging
* Run tasks in child processes so they can be killed

## Decision Outcome

Chosen option: "Isolate user code and hand every decision to
caller-supplied policy", because it is the only option that keeps loom
correct around untrusted code without loom deciding what tasks should do.

* **Isolation.** Every call into application code (tasks, triggers, retry
  policies, hooks) runs on its own goroutine under `recover`. A panic or
  `runtime.Goexit` becomes `ErrPanic` for a task and is dropped for a hook.
  Error chains are walked by loom itself with a depth limit and `recover`,
  so a hostile `Unwrap` cannot hang or crash it.
* **Policy.** Retries come only from a `RetryPolicy` the caller sets. With
  none, a failed task is not retried. An interrupted worker cancels its
  task's context with the cause `ErrInterrupted`, and the caller's policy
  decides whether that task runs again.
* **Waiting.** loom cancels a task's context on timeout, interrupt, or
  cancellation, then waits for the task to return. A task that ignores its
  context keeps its worker until it returns. `Run` and `Shutdown` still
  honor their own contexts and return on time.
* **Hooks.** Events are delivered in order on a separate goroutine, so a
  slow or blocked hook never stalls a task. A hook can run after `Run`
  returns, and the context it receives can already be canceled.

### Consequences

* Good, because no application function can crash or wedge loom itself.
* Good, because every behavior that affects a task is visible in the
  caller's own code.
* Bad, because a function that ignores its context holds a worker until it
  returns, and loom can only report it.
* Bad, because a goroutine an application function starts on its own is
  outside loom, and a panic there still ends the process. Go offers no way
  to catch it.
* Bad, because a hook panic has no caller to return to, so it is recovered
  and dropped.

### Confirmation

`loom/hostile_test.go` runs tasks, triggers, retry policies, and hooks that
panic, call `runtime.Goexit`, block, ignore cancellation, and return
self-referencing and panicking errors, all under `-race` and at 100%
statement coverage.

## Pros and Cons of the Options

### Built-in policies

* Good, because the shortest call sites need no policy code.
* Bad, because every built-in choice (requeue, retry count, log format) is
  wrong for some application and needs an escape hatch.

### Child processes

* Good, because a stuck or crashing task can be killed.
* Bad, because tasks lose shared memory and typed values, and every input
  and output needs serialization.
* Bad, because it is a different library: process management, IPC, and
  binaries the caller must ship.
