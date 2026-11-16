package loom

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
)

// Graph holds tasks and the dependencies between them. Build it with New
// and Add, then call Run any number of times, concurrently if needed.
//
// The zero value is an empty, usable graph with no options. A Graph is safe
// for concurrent use. Add after the first Run is a mistake that Validate
// and every later Run report.
type Graph struct {
	mu     sync.Mutex
	cfg    graphConfig
	nodes  []*node
	names  map[string]bool
	errs   problems
	frozen bool
}

// node is one task in a graph.
type node struct {
	graph    *Graph
	name     string
	index    int
	run      func(ctx context.Context, in *Inputs) (any, error)
	parents  []*node  // declared with After
	names    []string // declared with AfterName, resolved when the graph runs
	settings settings
}

// Func is the shape of every task: plain Go that receives its parents'
// values through in and returns its own value.
type Func[T any] func(ctx context.Context, in *Inputs) (T, error)

// Task is a typed handle to one task in a graph. Its value has type T. Pass
// it to After to depend on it, and to Get or Lookup to read its value.
type Task[T any] struct {
	n *node
}

// Name returns the task's name, or "" for a nil or zero-value Task.
func (t *Task[T]) Name() string {
	if n := t.node(); n != nil {
		return n.name
	}
	return ""
}

func (t *Task[T]) node() *node {
	if t == nil {
		return nil
	}
	return t.n
}

// Ref is any task handle, whatever its value type. Every *Task[T] is a Ref.
type Ref interface {
	// Name returns the task's name.
	Name() string

	node() *node
}

// New returns an empty graph configured by opts. Option mistakes are
// reported by Validate and Run.
func New(opts ...GraphOption) *Graph {
	g := &Graph{}
	for _, o := range opts {
		if o == nil {
			g.cfg.add(invalid("nil graph option"))
			continue
		}
		o.applyGraph(&g.cfg)
	}
	return g
}

// Add puts a task named name in g and returns its handle. Mistakes (a nil
// graph or function, an empty or duplicate name, a parent from another
// graph, an unknown parent name, a cycle, an invalid option, or an Add after
// the first Run) are reported by Validate and Run, before any task runs.
func Add[T any](g *Graph, name string, fn Func[T], opts ...TaskOption) *Task[T] {
	var cfg taskConfig
	for _, o := range opts {
		if o == nil {
			cfg.add(invalid("nil task option"))
			continue
		}
		o.applyTask(&cfg)
	}
	n := &node{graph: g, name: name, names: cfg.afterNames, settings: cfg.settings}
	if fn != nil {
		n.run = func(ctx context.Context, in *Inputs) (any, error) { return fn(ctx, in) }
	}
	t := &Task[T]{n: n}
	if g == nil {
		return t
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	bad := func(format string, args ...any) {
		g.errs.add(taskError(name, format, args...))
	}
	if g.frozen {
		bad("added after Run")
		return t
	}
	switch {
	case name == "":
		bad("empty name")
	case g.names[name]:
		bad("duplicate name")
	}
	if fn == nil {
		bad("nil function")
	}
	for _, err := range cfg.problems {
		g.errs.add(fmt.Errorf("task %s: %w", strconv.Quote(name), err))
	}
	seen := map[*node]bool{}
	for _, ref := range cfg.after {
		var p *node
		if ref != nil {
			p = ref.node()
		}
		switch {
		case p == nil:
			bad("nil parent")
		case p.graph != g:
			bad("parent %q belongs to another graph", p.name)
		case !seen[p]:
			seen[p] = true
			n.parents = append(n.parents, p)
		}
	}
	if g.names == nil {
		g.names = map[string]bool{}
	}
	g.names[name] = true
	n.index = len(g.nodes)
	g.nodes = append(g.nodes, n)
	return t
}

// Validate reports every mistake Run would refuse, joined, without running
// anything. Graph mistakes, including a cycle, match ErrInvalidGraph, and
// option mistakes match ErrInvalidOption.
func (g *Graph) Validate() error {
	if g == nil {
		return fmt.Errorf("%w: nil graph", ErrInvalidGraph)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	_, errs := g.resolve()
	return errors.Join(errs...)
}

// taskError reports a mistake in the task named name.
func taskError(name, format string, args ...any) error {
	return fmt.Errorf("%w: task %s: "+format, append([]any{ErrInvalidGraph, strconv.Quote(name)}, args...)...)
}
