package loom_test

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/brian-nunez/bkit/loom"
)

// edge declares one parent of a task. handle asks for After with the
// parent's handle, which is possible only once the parent has been added;
// otherwise the edge is declared with AfterName.
type edge struct {
	parent int
	handle bool
}

// testGraph is a loom graph built from a model the tests reason about
// without loom. Task i is named "t<i>".
type testGraph struct {
	graph *loom.Graph
	tasks []*loom.Task[uint64]

	// seq orders task starts and ends: starts[i] and ends[i] hold the
	// sequence numbers of task i's start and end, and runs[i] counts how
	// many times task i ran.
	seq    atomic.Uint64
	starts []atomic.Uint64
	ends   []atomic.Uint64
	runs   []atomic.Int32
}

func taskName(i int) string {
	return "t" + strconv.Itoa(i)
}

// uniqueParents returns the distinct parents declared in decls, per task.
func uniqueParents(decls [][]edge) [][]int {
	parents := make([][]int, len(decls))
	for i, es := range decls {
		for _, e := range es {
			parents[i] = append(parents[i], e.parent)
		}
		slices.Sort(parents[i])
		parents[i] = slices.Compact(parents[i])
	}
	return parents
}

// baseValue is the part of task i's value that does not depend on its
// parents.
func baseValue(i int) uint64 {
	return uint64(i+1) * 0x9E3779B97F4A7C15
}

// mixParent folds a parent's value into a task's value.
func mixParent(v uint64, parent int, pv uint64) uint64 {
	return v + pv*uint64(parent+2)
}

// expectedValues computes, without loom, the value of every task that is
// not blocked. Blocked tasks never run and keep zero.
func expectedValues(parents [][]int, blocked []bool) []uint64 {
	want := make([]uint64, len(parents))
	done := make([]bool, len(parents))
	var visit func(i int) uint64
	visit = func(i int) uint64 {
		if !done[i] {
			v := baseValue(i)
			for _, p := range parents[i] {
				v = mixParent(v, p, visit(p))
			}
			want[i], done[i] = v, true
		}
		return want[i]
	}
	for i := range parents {
		if !blocked[i] {
			visit(i)
		}
	}
	return want
}

// buildGraph adds the tasks in decls to a new graph, in the given order.
// Each task checks, as it runs, that every parent ended before it started
// and that Get returns the parent's expected value, and fails otherwise.
func buildGraph(t *testing.T, decls [][]edge, order []int, want []uint64, opts ...loom.GraphOption) *testGraph {
	t.Helper()
	n := len(decls)
	tg := &testGraph{
		graph:  loom.New(opts...),
		tasks:  make([]*loom.Task[uint64], n),
		starts: make([]atomic.Uint64, n),
		ends:   make([]atomic.Uint64, n),
		runs:   make([]atomic.Int32, n),
	}
	parents := uniqueParents(decls)
	added := make([]bool, n)
	for _, i := range order {
		var taskOpts []loom.TaskOption
		for _, e := range decls[i] {
			if e.handle && added[e.parent] {
				taskOpts = append(taskOpts, loom.After(tg.tasks[e.parent]))
			} else {
				taskOpts = append(taskOpts, loom.AfterName(taskName(e.parent)))
			}
		}
		tg.tasks[i] = loom.Add(tg.graph, taskName(i), tg.taskFunc(i, parents[i], want), taskOpts...)
		added[i] = true
	}
	return tg
}

// taskFunc returns task i's function. It reads tg.tasks when it runs, so
// handles added after task i resolve.
func (tg *testGraph) taskFunc(i int, parents []int, want []uint64) loom.Func[uint64] {
	return func(_ context.Context, in *loom.Inputs) (uint64, error) {
		tg.runs[i].Add(1)
		start := tg.seq.Add(1)
		tg.starts[i].Store(start)
		v := baseValue(i)
		for _, p := range parents {
			if end := tg.ends[p].Load(); end == 0 || end > start {
				return 0, fmt.Errorf("%s started at %d before parent %s ended (end %d)", taskName(i), start, taskName(p), end)
			}
			got := loom.Get(in, tg.tasks[p])
			if got != want[p] {
				return 0, fmt.Errorf("%s: Get(%s) = %d, want %d", taskName(i), taskName(p), got, want[p])
			}
			v = mixParent(v, p, got)
		}
		tg.ends[i].Store(tg.seq.Add(1))
		return v, nil
	}
}

// checkRun asserts that every task that is not blocked ran once and
// returned its expected value, and that every blocked task never ran and
// stayed Pending.
func (tg *testGraph) checkRun(t *testing.T, res *loom.Result, want []uint64, blocked []bool) {
	t.Helper()
	for i, task := range tg.tasks {
		state, runs := res.State(task), tg.runs[i].Load()
		if blocked[i] {
			if state != loom.Pending || runs != 0 {
				t.Errorf("blocked %s: state %v after %d runs, want pending after 0", taskName(i), state, runs)
			}
			continue
		}
		if state != loom.Succeeded || runs != 1 {
			t.Errorf("%s: state %v after %d runs, want succeeded after 1: %v", taskName(i), state, runs, res.Err(task))
			continue
		}
		if got := loom.Get(res, task); got != want[i] {
			t.Errorf("%s: value %d, want %d", taskName(i), got, want[i])
		}
	}
}

// randomDAG returns n tasks whose edges go only from a lower index to a
// higher one, with a random density, each declared by handle or by name at
// random and in a shuffled order.
func randomDAG(rng *rand.Rand, n int) [][]edge {
	density := rng.Float64() * 0.4
	decls := make([][]edge, n)
	for child := range n {
		for parent := range child {
			if rng.Float64() < density {
				decls[child] = append(decls[child], edge{parent: parent, handle: rng.IntN(2) == 0})
			}
		}
		rng.Shuffle(len(decls[child]), func(a, b int) {
			decls[child][a], decls[child][b] = decls[child][b], decls[child][a]
		})
	}
	return decls
}

// addIndexes maps each task to its position in the order it was added.
func addIndexes(order []int) []int {
	at := make([]int, len(order))
	for pos, i := range order {
		at[i] = pos
	}
	return at
}

// reportedCycle parses the cycle in err and checks it is a real cycle of
// the graph: each task is a parent of the next, it ends where it starts,
// no task repeats in between, and it starts with the earliest-added task
// in it. It returns the tasks in the cycle, without the repeated first.
func reportedCycle(t *testing.T, err error, parents [][]int, addIndex []int) []int {
	t.Helper()
	if !errors.Is(err, loom.ErrCycle) || !errors.Is(err, loom.ErrInvalidGraph) {
		t.Fatalf("error %v does not match both ErrCycle and ErrInvalidGraph", err)
	}
	rest, ok := strings.CutPrefix(err.Error(), "loom: invalid graph: cycle: ")
	if !ok {
		t.Fatalf("error %q is not exactly one cycle", err)
	}
	var cycle []int
	for name := range strings.SplitSeq(rest, " -> ") {
		digits, ok := strings.CutPrefix(name, "t")
		i, convErr := strconv.Atoi(digits)
		if !ok || convErr != nil || i < 0 || i >= len(parents) {
			t.Fatalf("cycle %q names unknown task %q", rest, name)
		}
		cycle = append(cycle, i)
	}
	last := len(cycle) - 1
	if last < 1 || cycle[0] != cycle[last] {
		t.Fatalf("cycle %q does not end where it starts", rest)
	}
	body := cycle[:last]
	seen := map[int]bool{}
	for k, i := range body {
		if seen[i] {
			t.Fatalf("cycle %q repeats %s", rest, taskName(i))
		}
		seen[i] = true
		if next := cycle[k+1]; !slices.Contains(parents[next], i) {
			t.Fatalf("cycle %q: %s is not a parent of %s", rest, taskName(i), taskName(next))
		}
		if addIndex[i] < addIndex[cycle[0]] {
			t.Fatalf("cycle %q starts with %s, but %s was added earlier", rest, taskName(cycle[0]), taskName(i))
		}
	}
	return body
}

// reachable reports, for each task, the tasks reachable from it by
// following one or more edges from parent to child.
func reachable(parents [][]int) [][]bool {
	n := len(parents)
	reach := make([][]bool, n)
	for i := range reach {
		reach[i] = make([]bool, n)
	}
	for child, ps := range parents {
		for _, p := range ps {
			reach[p][child] = true
		}
	}
	for k := range n {
		for i := range n {
			if !reach[i][k] {
				continue
			}
			for j := range n {
				reach[i][j] = reach[i][j] || reach[k][j]
			}
		}
	}
	return reach
}

// Every random DAG validates, and every task runs once, after all its
// parents end, reading their values.
func TestPropertyAcyclicGraphsRun(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	for g := range 300 {
		n := 1 + rng.IntN(40)
		decls := randomDAG(rng, n)
		order := rng.Perm(n)
		blocked := make([]bool, n)
		want := expectedValues(uniqueParents(decls), blocked)
		tg := buildGraph(t, decls, order, want)
		if err := tg.graph.Validate(); err != nil {
			t.Fatalf("graph %d (n=%d): Validate = %v", g, n, err)
		}
		res, err := tg.graph.Run(t.Context())
		if err != nil {
			t.Fatalf("graph %d (n=%d): Run = %v", g, n, err)
		}
		tg.checkRun(t, res, want, blocked)
		if got := tg.seq.Load(); got != uint64(2*n) {
			t.Fatalf("graph %d: %d task starts and ends, want %d", g, got, 2*n)
		}
	}
}

// A random DAG plus one edge that closes a cycle fails Validate and Run
// with ErrCycle, naming a real cycle through that edge, and runs nothing.
func TestPropertyCyclicGraphsRejected(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	for g := range 300 {
		n := 1 + rng.IntN(30)
		decls := randomDAG(rng, n)
		reach := reachable(uniqueParents(decls))

		// Close a cycle: u depends on v, where v is u or a descendant of u.
		u := rng.IntN(n)
		candidates := []int{u}
		for v := range n {
			if reach[u][v] {
				candidates = append(candidates, v)
			}
		}
		v := candidates[rng.IntN(len(candidates))]
		decls[u] = append(decls[u], edge{parent: v})

		order := rng.Perm(n)
		parents, addIndex := uniqueParents(decls), addIndexes(order)
		tg := buildGraph(t, decls, order, make([]uint64, n))

		cycle := reportedCycle(t, tg.graph.Validate(), parents, addIndex)
		k := slices.Index(cycle, v)
		if k < 0 || cycle[(k+1)%len(cycle)] != u {
			t.Fatalf("graph %d: cycle %v skips the closing edge %s -> %s", g, cycle, taskName(v), taskName(u))
		}
		res, err := tg.graph.Run(t.Context())
		if got := reportedCycle(t, err, parents, addIndex); !slices.Equal(got, cycle) {
			t.Fatalf("graph %d: Run reports cycle %v, Validate %v", g, got, cycle)
		}
		for i, task := range tg.tasks {
			if res.State(task) != loom.Pending {
				t.Fatalf("graph %d: %s is %v after a refused run", g, taskName(i), res.State(task))
			}
		}
		if tg.seq.Load() != 0 {
			t.Fatalf("graph %d: tasks ran in a refused run", g)
		}
	}
}

// workerCountResult is what one run of the worker-count property
// observes.
type workerCountResult struct {
	states []loom.State
	values []uint64
	err    string
}

// The same graph, with failures, skips, and mixed trigger rules, has the
// same outcome on one worker, on many, and on a private pool.
func TestPropertyWorkerCountInvariant(t *testing.T) {
	newPool := func(workers int) *loom.Pool {
		t.Helper()
		p, err := loom.NewPool(loom.WithWorkers(workers))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := p.Close(); err != nil {
				t.Error(err)
			}
		})
		return p
	}
	one, many := newPool(1), newPool(16)
	triggers := []loom.Trigger{loom.AllSuccess, loom.AllDone, loom.NoneFailed, loom.OneSuccess, loom.OneFailed, loom.AllFailed}

	rng := rand.New(rand.NewPCG(1, 2))
	for g := range 100 {
		n := 1 + rng.IntN(30)
		decls := randomDAG(rng, n)
		parents := uniqueParents(decls)
		graph := loom.New()
		tasks := make([]*loom.Task[uint64], n)
		for i := range n {
			outcome := rng.IntN(10)
			fn := func(_ context.Context, in *loom.Inputs) (uint64, error) {
				v := baseValue(i)
				for _, p := range parents[i] {
					if pv, ok := loom.Lookup(in, tasks[p]); ok {
						v = mixParent(v, p, pv)
					}
				}
				switch outcome {
				case 0:
					return 0, errors.New("fail " + taskName(i))
				case 1:
					return 0, loom.ErrSkip
				}
				return v, nil
			}
			opts := []loom.TaskOption{loom.WithTrigger(triggers[rng.IntN(len(triggers))])}
			for _, p := range parents[i] {
				opts = append(opts, loom.After(tasks[p]))
			}
			tasks[i] = loom.Add(graph, taskName(i), fn, opts...)
		}

		run := func(opts ...loom.RunOption) workerCountResult {
			t.Helper()
			res, err := graph.Run(t.Context(), opts...)
			out := workerCountResult{err: fmt.Sprint(err)}
			for _, task := range tasks {
				out.states = append(out.states, res.State(task))
				out.values = append(out.values, loom.Get(res, task))
			}
			return out
		}
		base := run(loom.WithPool(one))
		for name, got := range map[string]workerCountResult{
			"16 workers":   run(loom.WithPool(many)),
			"private pool": run(),
		} {
			if !slices.Equal(got.states, base.states) || !slices.Equal(got.values, base.values) || got.err != base.err {
				t.Fatalf("graph %d: %s differs from 1 worker:\n got %+v\nwant %+v", g, name, got, base)
			}
		}
	}
}
