package loom_test

import (
	"testing"

	"github.com/brian-nunez/bkit/loom"
)

// maxFuzzTasks and maxFuzzEdges bound the graphs FuzzGraphCycles decodes.
const (
	maxFuzzTasks = 16
	maxFuzzEdges = 64
)

// decodeGraph turns data into a graph: the first byte picks the task count,
// and each following pair of bytes is an edge. The first byte of a pair
// picks the parent, and its high bit asks for After when the parent is
// already added. The second picks the child. Edges can repeat, close
// cycles, and point a task at itself.
func decodeGraph(data []byte) [][]edge {
	n := 1
	if len(data) > 0 {
		n += int(data[0]) % maxFuzzTasks
		data = data[1:]
	}
	decls := make([][]edge, n)
	for k := 0; k+1 < len(data) && k < 2*maxFuzzEdges; k += 2 {
		parent, child := int(data[k]&0x7f)%n, int(data[k+1])%n
		decls[child] = append(decls[child], edge{parent: parent, handle: data[k]&0x80 != 0})
	}
	return decls
}

// hasCycle reports whether the graph has a cycle, with a depth-first
// search that colors each task unvisited, on the current path, or done.
func hasCycle(parents [][]int) bool {
	const (
		unvisited = iota
		onPath
		done
	)
	color := make([]int, len(parents))
	var visit func(i int) bool
	visit = func(i int) bool {
		color[i] = onPath
		for _, p := range parents[i] {
			if color[p] == onPath || (color[p] == unvisited && visit(p)) {
				return true
			}
		}
		color[i] = done
		return false
	}
	for i := range parents {
		if color[i] == unvisited && visit(i) {
			return true
		}
	}
	return false
}

// blockedTasks reports the tasks in a cycle or downstream of one.
func blockedTasks(parents [][]int) []bool {
	reach := reachable(parents)
	blocked := make([]bool, len(parents))
	for i := range parents {
		if !reach[i][i] {
			continue
		}
		blocked[i] = true
		for j, ok := range reach[i] {
			blocked[j] = blocked[j] || ok
		}
	}
	return blocked
}

// FuzzGraphCycles checks Validate's cycle verdict against an independent
// depth-first search, checks that a reported cycle is real, and checks
// that a run with AllowCycles leaves exactly the tasks in or downstream of
// a cycle Pending while every other task succeeds with the right value.
func FuzzGraphCycles(f *testing.F) {
	for _, seed := range [][]byte{
		{},
		{0, 0, 0},                         // t0 -> t0
		{1, 0, 1, 1, 0},                   // t0 -> t1 -> t0
		{2, 0x80, 1, 0x81, 2},             // t0 -> t1 -> t2 by handle
		{2, 2, 0, 0, 1, 1, 2},             // t0 -> t1 -> t2 -> t0
		{4, 0, 1, 1, 2, 2, 1, 2, 3, 0, 4}, // t1 -> t2 -> t1 with a tail, and free tasks
		{15, 0x80, 15, 3, 7, 7, 3, 9, 9, 0x8a, 11},
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		decls := decodeGraph(data)
		n := len(decls)
		order := make([]int, n)
		for i := range order {
			order[i] = i
		}
		parents := uniqueParents(decls)
		blocked := blockedTasks(parents)
		want := expectedValues(parents, blocked)

		strict := buildGraph(t, decls, order, want)
		err := strict.graph.Validate()
		if cyclic := hasCycle(parents); cyclic != (err != nil) {
			t.Fatalf("depth-first search finds a cycle: %v, Validate = %v", cyclic, err)
		}
		if err != nil {
			reportedCycle(t, err, parents, order)
		}

		loose := buildGraph(t, decls, order, want, loom.AllowCycles())
		if err := loose.graph.Validate(); err != nil {
			t.Fatalf("Validate with AllowCycles = %v", err)
		}
		res, err := loose.graph.Run(t.Context())
		if err != nil {
			t.Fatalf("Run with AllowCycles = %v", err)
		}
		loose.checkRun(t, res, want, blocked)
	})
}
