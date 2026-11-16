package loom

import (
	"fmt"
	"slices"
	"strings"
)

// resolve returns every task's parents, from handles and names, with every
// mistake that stops the graph from running: option and build mistakes,
// unknown parent names, and, unless the graph allows cycles, a cycle. The
// caller holds g.mu.
func (g *Graph) resolve() (map[*node][]*node, []error) {
	errs := slices.Concat(g.cfg.problems, g.errs)
	byName := make(map[string]*node, len(g.nodes))
	for _, n := range g.nodes {
		if _, dup := byName[n.name]; !dup {
			byName[n.name] = n
		}
	}
	parents := make(map[*node][]*node, len(g.nodes))
	for _, n := range g.nodes {
		ps := slices.Clone(n.parents)
		for _, name := range n.names {
			p, ok := byName[name]
			switch {
			case !ok:
				errs = append(errs, taskError(n.name, "unknown parent %q", name))
			case !slices.Contains(ps, p):
				ps = append(ps, p)
			}
		}
		parents[n] = ps
	}
	if !g.cfg.allowCycles {
		if cycle := findCycle(g.nodes, parents); cycle != nil {
			errs = append(errs, fmt.Errorf("%w: cycle: %s", ErrInvalidGraph, strings.Join(cycle, " -> ")))
		}
	}
	return parents, errs
}

// findCycle runs Kahn's algorithm. It repeatedly removes tasks whose parents
// have all been removed, starting from tasks with no parents. Tasks that
// remain are in a cycle or downstream of one. findCycle returns one cycle as
// task names, each a parent of the next, starting and ending with the
// earliest-added task in it, or nil when every task was removed.
func findCycle(nodes []*node, parents map[*node][]*node) []string {
	remaining := make(map[*node]int, len(nodes)) // parents not yet removed
	children := make(map[*node][]*node, len(nodes))
	var queue []*node
	for _, n := range nodes {
		remaining[n] = len(parents[n])
		for _, p := range parents[n] {
			children[p] = append(children[p], n)
		}
		if remaining[n] == 0 {
			queue = append(queue, n)
		}
	}
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		for _, c := range children[n] {
			remaining[c]--
			if remaining[c] == 0 {
				queue = append(queue, c)
			}
		}
	}

	// Every task that remains has a parent that remains. Walking from one
	// remaining parent to the next must therefore revisit a task, and the
	// tasks between the two visits form a cycle.
	i := slices.IndexFunc(nodes, func(n *node) bool { return remaining[n] > 0 })
	if i < 0 {
		return nil
	}
	visited := map[*node]int{} // position in path
	var path []*node           // each task followed by one of its parents
	n := nodes[i]
	for {
		if at, ok := visited[n]; ok {
			return cycleNames(path[at:])
		}
		visited[n] = len(path)
		path = append(path, n)
		next := slices.IndexFunc(parents[n], func(p *node) bool { return remaining[p] > 0 })
		n = parents[n][next]
	}
}

// cycleNames turns a walk from child to parent into names from parent to
// child, starting and ending with the earliest-added task.
func cycleNames(walk []*node) []string {
	order := slices.Clone(walk)
	slices.Reverse(order)
	first := 0
	for i, n := range order {
		if n.index < order[first].index {
			first = i
		}
	}
	order = append(order[first:], order[:first]...)
	names := make([]string, 0, len(order)+1)
	for _, n := range order {
		names = append(names, n.name)
	}
	return append(names, names[0])
}
