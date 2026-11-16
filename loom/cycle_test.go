package loom_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/brian-nunez/bkit/loom"
)

// Parents named before they exist resolve when the graph runs.
func ExampleAfterName() {
	g := loom.New()
	var users *loom.Task[[]User]
	count := loom.Add(g, "count", func(ctx context.Context, in *loom.Inputs) (int, error) {
		return len(loom.Get(in, users)), nil
	}, loom.AfterName("users"))
	users = loom.Add(g, "users", fetchUsers)

	res, err := g.Run(context.Background())
	fmt.Println(loom.Get(res, count), err)
	// Output: 2 <nil>
}

// Run refuses a graph with a cycle before any task starts.
func Example_cycle() {
	g := loom.New()
	loom.Add(g, "a", value(1), loom.AfterName("c"))
	loom.Add(g, "b", value(2), loom.AfterName("a"))
	loom.Add(g, "c", value(3), loom.AfterName("b"))

	_, err := g.Run(context.Background())
	fmt.Println(errors.Is(err, loom.ErrCycle), errors.Is(err, loom.ErrInvalidGraph))
	fmt.Println(err)
	// Output:
	// true true
	// loom: invalid graph: cycle: a -> b -> c -> a
}

// With AllowCycles, tasks in a cycle never become ready and stay Pending,
// along with everything downstream of them. Everything else runs.
func ExampleAllowCycles() {
	g := loom.New(loom.AllowCycles())
	a := loom.Add(g, "a", value(1), loom.AfterName("b"))
	b := loom.Add(g, "b", value(2), loom.AfterName("a"))
	after := loom.Add(g, "after", value(3), loom.After(b))
	free := loom.Add(g, "free", value(4))

	res, err := g.Run(context.Background())
	fmt.Println(res.State(a), res.State(b), res.State(after), res.State(free), err)
	// Output: pending pending pending succeeded <nil>
}

func TestCycles(t *testing.T) {
	cases := []struct {
		name  string
		build func(g *loom.Graph)
		want  string
	}{
		{
			name: "self",
			build: func(g *loom.Graph) {
				loom.Add(g, "a", value(1), loom.AfterName("a"))
			},
			want: "cycle: a -> a",
		},
		{
			name: "handles and names",
			build: func(g *loom.Graph) {
				a := loom.Add(g, "a", value(1), loom.AfterName("b"))
				loom.Add(g, "b", value(2), loom.After(a))
			},
			want: "cycle: a -> b -> a",
		},
		{
			name: "downstream of a cycle",
			build: func(g *loom.Graph) {
				root := loom.Add(g, "root", value(0))
				tail := loom.Add(g, "tail", value(1), loom.AfterName("x"))
				loom.Add(g, "x", value(2), loom.After(root), loom.AfterName("y"))
				loom.Add(g, "y", value(3), loom.AfterName("x"))
				loom.Add(g, "z", value(4), loom.After(tail))
			},
			want: "cycle: x -> y -> x",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := loom.New()
			c.build(g)
			wantErr(t, g.Validate(), loom.ErrCycle, c.want)
			res, err := g.Run(context.Background())
			wantErr(t, err, loom.ErrCycle, c.want)
			wantErr(t, err, loom.ErrInvalidGraph, c.want)
			if strings.Count(err.Error(), "cycle:") != 1 {
				t.Errorf("want one cycle reported: %v", err)
			}
			if res == nil {
				t.Fatal("nil Result")
			}
		})
	}
}

func TestAfterName(t *testing.T) {
	g := loom.New()
	loom.Add(g, "a", value(1))
	loom.Add(g, "typo", value(2), loom.AfterName("aa"))
	err := g.Validate()
	wantErr(t, err, loom.ErrInvalidGraph, `task "typo": unknown parent "aa"`)
	if errors.Is(err, loom.ErrCycle) {
		t.Errorf("an unknown parent matches ErrCycle: %v", err)
	}

	// The same parent by handle and by name counts once.
	ok := loom.New()
	p := loom.Add(ok, "p", value(5))
	twice := loom.Add(ok, "twice", func(ctx context.Context, in *loom.Inputs) (int, error) {
		return loom.Get(in, p) * 2, nil
	}, loom.After(p), loom.AfterName("p", "p"))
	res, err := ok.Run(context.Background())
	if err != nil || loom.Get(res, twice) != 10 {
		t.Fatalf("%v %v", loom.Get(res, twice), err)
	}

	// With a duplicate task name, a name resolves to the first task.
	dup := loom.New(loom.AllowCycles())
	first := loom.Add(dup, "x", value(1))
	loom.Add(dup, "x", value(2))
	loom.Add(dup, "child", func(ctx context.Context, in *loom.Inputs) (int, error) {
		return loom.Get(in, first), nil
	}, loom.AfterName("x"))
	if err := dup.Validate(); !errors.Is(err, loom.ErrInvalidGraph) || errors.Is(err, loom.ErrCycle) || strings.Contains(err.Error(), "unknown parent") {
		t.Fatalf("Validate = %v", err)
	}
}
