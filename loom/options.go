package loom

import (
	"fmt"
	"runtime"
	"time"
)

// GraphOption configures New. Settings that tasks also accept (WithRetry,
// WithTimeout, WithTrigger) become defaults for every task in the graph.
type GraphOption interface {
	applyGraph(c *graphConfig)
}

// TaskOption configures one Add. Settings given here override the graph's
// defaults for this task.
type TaskOption interface {
	applyTask(c *taskConfig)
}

// RunOption configures one Run. Settings given here override the graph's
// for this run.
type RunOption interface {
	applyRun(c *runConfig)
}

// PoolOption configures NewPool.
type PoolOption interface {
	applyPool(c *poolConfig)
}

// ShrinkOption configures one Shrink.
type ShrinkOption interface {
	applyShrink(c *shrinkConfig)
}

// problems collects option and build errors for later reporting.
type problems []error

func (p *problems) add(err error) {
	if err != nil {
		*p = append(*p, err)
	}
}

// settings are the per-task settings a graph sets as defaults and a task
// overrides.
type settings struct {
	retry    RetryPolicy
	retrySet bool
	timeout  time.Duration
	trigger  Trigger
}

// over returns s with every setting t sets replacing the one in s.
func (s settings) over(t settings) settings {
	if t.retrySet {
		s.retry, s.retrySet = t.retry, true
	}
	if t.timeout > 0 {
		s.timeout = t.timeout
	}
	if t.trigger != nil {
		s.trigger = t.trigger
	}
	return s
}

type graphConfig struct {
	problems
	allowCycles bool
	name        string
	pool        *Pool
	hooks       []Hooks
	failFast    bool
	defaults    settings
}

type taskConfig struct {
	problems
	after      []Ref
	afterNames []string
	settings   settings
}

type runConfig struct {
	problems
	pool     *Pool
	hooks    []Hooks
	failFast bool
	resume   *Result
}

type poolConfig struct {
	problems
	workers int
	hooks   []Hooks
}

type shrinkConfig struct {
	interrupt bool
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrInvalidOption}, args...)...)
}

// settingOption is accepted by New, as a graph default, and by Add, as a
// task override.
type settingOption func(s *settings) error

func (o settingOption) applyGraph(c *graphConfig) { c.add(o(&c.defaults)) }
func (o settingOption) applyTask(c *taskConfig)   { c.add(o(&c.settings)) }

// runSetting is accepted by New, for every run, and by Run, for one run.
type runSetting struct {
	graph func(c *graphConfig)
	run   func(c *runConfig)
}

func (o runSetting) applyGraph(c *graphConfig) { o.graph(c) }
func (o runSetting) applyRun(c *runConfig)     { o.run(c) }

// hooksOption is accepted by New, Run, and NewPool.
type hooksOption struct{ hooks Hooks }

func (o hooksOption) applyGraph(c *graphConfig) { c.hooks = append(c.hooks, o.hooks) }
func (o hooksOption) applyRun(c *runConfig)     { c.hooks = append(c.hooks, o.hooks) }
func (o hooksOption) applyPool(c *poolConfig)   { c.hooks = append(c.hooks, o.hooks) }

type graphOptionFunc func(c *graphConfig)

func (f graphOptionFunc) applyGraph(c *graphConfig) { f(c) }

type taskOptionFunc func(c *taskConfig)

func (f taskOptionFunc) applyTask(c *taskConfig) { f(c) }

type runOptionFunc func(c *runConfig)

func (f runOptionFunc) applyRun(c *runConfig) { f(c) }

type poolOptionFunc func(c *poolConfig)

func (f poolOptionFunc) applyPool(c *poolConfig) { f(c) }

type shrinkOptionFunc func(c *shrinkConfig)

func (f shrinkOptionFunc) applyShrink(c *shrinkConfig) { f(c) }

// After declares the task's parents. The task starts once every parent has
// a final state and its trigger rule agrees. A parent must come from the
// same graph.
func After(parents ...Ref) TaskOption {
	return taskOptionFunc(func(c *taskConfig) { c.after = append(c.after, parents...) })
}

// AfterName declares the task's parents by name. Names are resolved when
// the graph runs, so they can name tasks added later. A name that matches
// no task is ErrInvalidGraph. Read a named parent's value with its handle,
// as with After.
func AfterName(names ...string) TaskOption {
	return taskOptionFunc(func(c *taskConfig) { c.afterNames = append(c.afterNames, names...) })
}

// AllowCycles turns off the cycle check, so Run and Validate no longer
// report ErrCycle. A
// task in a cycle waits for a parent that waits for it, so it never becomes
// ready: it stays Pending, along with every task downstream of it, and the
// run returns once everything else is done.
func AllowCycles() GraphOption {
	return graphOptionFunc(func(c *graphConfig) { c.allowCycles = true })
}

// WithRetry sets the retry policy. On New it is the default for every task,
// and on Add it overrides the default for one task. WithRetry(nil) turns
// retries off. Without WithRetry, tasks are not retried.
func WithRetry(p RetryPolicy) settingOption {
	return func(s *settings) error {
		s.retry, s.retrySet = p, true
		return nil
	}
}

// WithTimeout limits each attempt to d. When d passes, the task's context
// is canceled with the cause ErrTimeout. On New it is the default for every
// task, and on Add it overrides the default. By default attempts have no
// timeout. A d of zero or less is ErrInvalidOption.
func WithTimeout(d time.Duration) settingOption {
	return func(s *settings) error {
		if d <= 0 {
			return invalid("WithTimeout needs a positive duration")
		}
		s.timeout = d
		return nil
	}
}

// WithTrigger sets the trigger rule. On New it is the default for every
// task, and on Add it overrides the default. The default is AllSuccess. A
// nil t is ErrInvalidOption.
func WithTrigger(t Trigger) settingOption {
	return func(s *settings) error {
		if t == nil {
			return invalid("WithTrigger needs a trigger")
		}
		s.trigger = t
		return nil
	}
}

// WithName names the graph. Every event the graph emits carries the name.
// An empty name is ErrInvalidOption.
func WithName(name string) GraphOption {
	return graphOptionFunc(func(c *graphConfig) {
		if name == "" {
			c.add(invalid("WithName needs a name"))
			return
		}
		c.name = name
	})
}

// WithPool runs tasks on p. On New it applies to every run, and on Run to
// one run. Without it, each Run uses a private pool with
// runtime.GOMAXPROCS(0) workers. A nil p is ErrInvalidOption.
func WithPool(p *Pool) runSetting {
	check := func(add func(error)) bool {
		if p == nil {
			add(invalid("WithPool needs a pool"))
			return false
		}
		return true
	}
	return runSetting{
		graph: func(c *graphConfig) {
			if check(c.add) {
				c.pool = p
			}
		},
		run: func(c *runConfig) {
			if check(c.add) {
				c.pool = p
			}
		},
	}
}

// FailFast stops a run at its first failed task: running tasks have their
// context canceled, and tasks that have not started end Canceled. On New it
// applies to every run, and on Run to one run.
func FailFast() runSetting {
	return runSetting{
		graph: func(c *graphConfig) { c.failFast = true },
		run:   func(c *runConfig) { c.failFast = true },
	}
}

// WithHooks registers callbacks for events. On New they receive the graph's
// events, on Run one run's events, and on NewPool the pool's worker events
// plus the task and run events of every graph that runs on the pool.
// Repeated WithHooks add more callbacks.
func WithHooks(h Hooks) hooksOption {
	return hooksOption{hooks: h}
}

// Resume reruns only the tasks that did not succeed or skip in prev, and
// reuses the values of those that succeeded. prev must come from a Run of
// the same graph, or Run fails with ErrInvalidOption.
func Resume(prev *Result) RunOption {
	return runOptionFunc(func(c *runConfig) {
		if prev == nil {
			c.add(invalid("Resume needs a result"))
			return
		}
		c.resume = prev
	})
}

// WithWorkers sets the pool's starting worker count. The default is
// runtime.GOMAXPROCS(0). Zero starts a paused pool that runs nothing until
// Grow. A negative n is ErrInvalidOption.
func WithWorkers(n int) PoolOption {
	return poolOptionFunc(func(c *poolConfig) {
		if n < 0 {
			c.add(invalid("WithWorkers needs zero or more workers"))
			return
		}
		c.workers = n
	})
}

// defaultWorkers is the worker count of a pool made without WithWorkers.
func defaultWorkers() int {
	return runtime.GOMAXPROCS(0)
}

// Interrupt makes Shrink cancel the removed workers' running tasks with the
// cause ErrInterrupted instead of letting them finish. What happens to an
// interrupted task is up to its RetryPolicy.
func Interrupt() ShrinkOption {
	return shrinkOptionFunc(func(c *shrinkConfig) { c.interrupt = true })
}
