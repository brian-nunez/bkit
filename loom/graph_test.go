package loom_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/brian-nunez/bkit/loom"
)

func value(v int) loom.Func[int] {
	return func(context.Context, *loom.Inputs) (int, error) { return v, nil }
}

func wantErr(t *testing.T, err, target error, contains string) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("error %v does not match %v", err, target)
	}
	if !strings.Contains(err.Error(), contains) {
		t.Fatalf("error %q does not contain %q", err, contains)
	}
}

func TestAddMistakes(t *testing.T) {
	other := loom.New()
	foreign := loom.Add(other, "foreign", value(1))

	g := loom.New()
	a := loom.Add(g, "a", value(1))
	loom.Add(g, "", value(1))
	loom.Add(g, "a", value(1))
	loom.Add[int](g, "nilfn", nil)
	loom.Add(g, "badopt", value(1), nil, loom.WithTimeout(0))
	loom.Add(g, "nilparent", value(1), loom.After(nil, (*loom.Task[int])(nil), &loom.Task[int]{}))
	loom.Add(g, "foreignparent", value(1), loom.After(foreign))
	loom.Add(g, "dupparent", value(1), loom.After(a, a))

	err := g.Validate()
	for _, want := range []string{
		`task "": empty name`,
		`task "a": duplicate name`,
		`task "nilfn": nil function`,
		`task "badopt": loom: invalid option: nil task option`,
		`task "badopt": loom: invalid option: WithTimeout needs a positive duration`,
		`task "nilparent": nil parent`,
		`task "foreignparent": parent "foreign" belongs to another graph`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Validate error lacks %q:\n%v", want, err)
		}
	}
	if strings.Contains(err.Error(), "dupparent") {
		t.Errorf("a parent passed twice is not a mistake: %v", err)
	}
	if _, runErr := g.Run(context.Background()); !errors.Is(runErr, loom.ErrInvalidGraph) {
		t.Errorf("Run error = %v, want ErrInvalidGraph", runErr)
	}
}

func TestAddAfterRun(t *testing.T) {
	g := loom.New()
	loom.Add(g, "a", value(1))
	if _, err := g.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	late := loom.Add(g, "late", value(2))
	wantErr(t, g.Validate(), loom.ErrInvalidGraph, `task "late": added after Run`)
	if late.Name() != "late" {
		t.Errorf("Name() = %q", late.Name())
	}
}

func TestNilAndZeroValues(t *testing.T) {
	var nilGraph *loom.Graph
	wantErr(t, nilGraph.Validate(), loom.ErrInvalidGraph, "nil graph")
	res, err := nilGraph.Run(context.Background())
	wantErr(t, err, loom.ErrInvalidGraph, "nil graph")
	if res == nil {
		t.Fatal("Run returned a nil Result")
	}

	orphan := loom.Add(nil, "orphan", value(1))
	if orphan.Name() != "orphan" {
		t.Errorf("Name() = %q", orphan.Name())
	}
	var zeroTask loom.Task[int]
	var nilTask *loom.Task[int]
	if zeroTask.Name() != "" || nilTask.Name() != "" {
		t.Error("nil and zero tasks have names")
	}

	var zero loom.Graph
	z := loom.Add(&zero, "z", value(7))
	res, err = zero.Run(context.Background())
	if err != nil || loom.Get(res, z) != 7 {
		t.Fatalf("zero-value graph: %v %v", loom.Get(res, z), err)
	}
	var nilCtx context.Context
	_, err = zero.Run(nilCtx)
	wantErr(t, err, loom.ErrInvalidOption, "nil context")
	_, err = zero.Run(context.Background(), nil)
	wantErr(t, err, loom.ErrInvalidOption, "nil run option")
}

func TestGraphOptionMistakes(t *testing.T) {
	g := loom.New(nil, loom.WithName(""), loom.WithPool(nil), loom.WithTrigger(nil), loom.WithTimeout(-1))
	err := g.Validate()
	for _, want := range []string{"nil graph option", "WithName needs a name", "WithPool needs a pool", "WithTrigger needs a trigger", "WithTimeout needs a positive duration"} {
		wantErr(t, err, loom.ErrInvalidOption, want)
	}

	ok := loom.New()
	loom.Add(ok, "a", value(1))
	_, err = ok.Run(context.Background(), loom.WithPool(nil), loom.Resume(nil))
	wantErr(t, err, loom.ErrInvalidOption, "WithPool needs a pool")
	wantErr(t, err, loom.ErrInvalidOption, "Resume needs a result")

	res, _ := loom.New().Run(context.Background())
	_, err = ok.Run(context.Background(), loom.Resume(res))
	wantErr(t, err, loom.ErrInvalidOption, "Resume needs a result from this graph")
}

func TestValuesAndResult(t *testing.T) {
	g := loom.New()
	a := loom.Add(g, "a", value(1))
	b := loom.Add(g, "b", func(context.Context, *loom.Inputs) (int, error) {
		return 0, errors.New("boom")
	})
	var iface loom.Task[error]
	ifaceTask := loom.Add(g, "iface", func(context.Context, *loom.Inputs) (error, error) { return nil, nil })
	stranger := loom.Add(loom.New(), "stranger", value(1))

	res, _ := g.Run(context.Background())
	if v, ok := loom.Lookup(res, ifaceTask); !ok || v != nil {
		t.Errorf("Lookup of a nil interface value = %v, %v", v, ok)
	}
	if _, ok := loom.Lookup(res, &iface); ok {
		t.Error("Lookup of a zero task reported a value")
	}
	if _, ok := loom.Lookup(res, b); ok {
		t.Error("Lookup of a failed task reported a value")
	}
	if _, ok := loom.Lookup[int](nil, a); ok {
		t.Error("Lookup on nil Values reported a value")
	}
	var nilRes *loom.Result
	var nilIn *loom.Inputs
	if loom.Get(nilRes, a) != 0 || loom.Get(nilIn, a) != 0 {
		t.Error("nil Result or Inputs returned a value")
	}
	if nilRes.State(a) != loom.Pending || res.State(nil) != loom.Pending || res.State(stranger) != loom.Pending {
		t.Error("unknown tasks are not Pending")
	}
	if res.Err(a) != nil || res.Err(nil) != nil || res.Err(stranger) != nil {
		t.Error("Err reported an error for a task that has none")
	}
	if res.Err(b) == nil {
		t.Error("Err lost the failed task's error")
	}
}
