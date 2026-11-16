package loom

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// queued reports how many jobs wait in p's queue.
func (p *Pool) queued() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.queue)
}

// Tasks still queued when the pool shuts down end Canceled with ErrClosed.
func TestShutdownRejectsQueuedTasks(t *testing.T) {
	p, _ := NewPool(WithWorkers(0))
	g := New(WithPool(p))
	a := Add(g, "a", func(context.Context, *Inputs) (int, error) { return 1, nil })
	b := Add(g, "b", func(context.Context, *Inputs) (int, error) { return 2, nil })

	type outcome struct {
		res *Result
		err error
	}
	done := make(chan outcome)
	go func() {
		res, err := g.Run(context.Background())
		done <- outcome{res, err}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for p.queued() < 2 {
		if time.Now().After(deadline) {
			t.Fatal("tasks were never queued")
		}
		time.Sleep(time.Millisecond)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	got := <-done
	if !errors.Is(got.err, ErrClosed) {
		t.Fatalf("err = %v", got.err)
	}
	for _, task := range []*Task[int]{a, b} {
		if got.res.State(task) != Canceled || !errors.Is(got.res.Err(task), ErrClosed) {
			t.Errorf("%s: %v %v", task.Name(), got.res.State(task), got.res.Err(task))
		}
	}
}

// TestMain fails the run when goroutines started by the tests are still
// running once every test has released what it blocked.
func TestMain(m *testing.M) {
	before := runtime.NumGoroutine()
	code := m.Run()
	if code == 0 {
		deadline := time.Now().Add(5 * time.Second)
		for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if n := runtime.NumGoroutine(); n > before {
			fmt.Fprintf(os.Stderr, "loom: %d goroutines leaked\n", n-before)
			code = 1
		}
	}
	os.Exit(code)
}

// The library never panics: no call to panic in non-test source.
func TestNoPanicInLibrary(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "panic" {
					t.Errorf("%s: library code calls panic", fset.Position(call.Pos()))
				}
			}
			return true
		})
	}
}
