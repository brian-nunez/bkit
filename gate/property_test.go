package gate

import (
	"errors"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// Property tests compare composed rules with an independent reference
// evaluator over generated rule trees. Every input is a uint64 and every leaf
// rule reads one of its low leafBits bits.

const (
	// leafBits is how many input bits leaf rules read. It stays small so
	// generated trees reuse bits and laws meet both outcomes often.
	leafBits = 6
	// maxArity bounds the operands of a generated n-ary node.
	maxArity = 4
	// propertyCases is how many generated cases each property checks.
	propertyCases = 3000
	// inputsPerCase is how many random inputs each generated case checks.
	inputsPerCase = 8
)

// newRand returns the fixed-seed generator every property uses, so a failure
// reproduces on every run.
func newRand() *rand.Rand {
	return rand.New(rand.NewPCG(1, 2))
}

// bitSet reports whether bit i of v is set.
func bitSet(v uint64, i int) bool {
	return v>>uint(i)&1 == 1
}

// bools returns the low m bits of v, least significant first.
func bools(v uint64, m int) []bool {
	out := make([]bool, m)
	for i := range m {
		out[i] = bitSet(v, i)
	}
	return out
}

// countTrue returns how many values in bs are true.
func countTrue(bs []bool) int {
	n := 0
	for _, b := range bs {
		if b {
			n++
		}
	}
	return n
}

// upTo returns the indexes 0 through n-1.
func upTo(n int) []int {
	out := make([]int, n)
	for i := range n {
		out[i] = i
	}
	return out
}

// stopAt returns the operand indexes a left-to-right evaluation that stops at
// the first operand equal to stop visits.
func stopAt(bs []bool, stop bool) []int {
	if i := slices.Index(bs, stop); i >= 0 {
		return upTo(i + 1)
	}
	return upTo(len(bs))
}

// atLeastDecided returns how many operands AtLeast(n, ...) must evaluate: the
// shortest prefix after which every completion of the remaining operands
// gives the same outcome.
func atLeastDecided(bs []bool, n int) int {
	for i := range len(bs) + 1 {
		passed := countTrue(bs[:i])
		remaining := len(bs) - i
		if passed >= n || passed+remaining < n {
			return i
		}
	}
	return len(bs)
}

// operands returns m rules where rule i reads bit i of its input and records
// i in calls.
func operands(calls *[]int, m int) []Rule[uint64] {
	rules := make([]Rule[uint64], m)
	for i := range m {
		rules[i] = func(v uint64) bool {
			*calls = append(*calls, i)
			return bitSet(v, i)
		}
	}
	return rules
}

// evalFlat evaluates build over m recording operands at input v and reports
// the result and the operand indexes it visited, in order.
func evalFlat(m int, v uint64, build func([]Rule[uint64]) Rule[uint64]) (bool, []int) {
	var calls []int
	got := build(operands(&calls, m))(v)
	return got, calls
}

// assertFlat fails t when a flat combinator's result or visited operands
// differ from the expected ones.
func assertFlat(t *testing.T, name string, v uint64, got bool, calls []int, want bool, wantCalls []int) {
	t.Helper()
	if got != want {
		t.Fatalf("%s at input %b = %v, want %v", name, v, got, want)
	}
	if !slices.Equal(calls, wantCalls) {
		t.Fatalf("%s at input %b evaluated operands %v, want %v", name, v, calls, wantCalls)
	}
}

// TestPropertyTruthTables checks every combinator against its boolean formula
// for every operand assignment, and checks that each one evaluates exactly the
// operands it needs, in argument order.
func TestPropertyTruthTables(t *testing.T) {
	for m := range 6 {
		for v := range uint64(1) << m {
			bs := bools(v, m)
			allTrue := !slices.Contains(bs, false)
			anyTrue := slices.Contains(bs, true)

			got, calls := evalFlat(m, v, func(rs []Rule[uint64]) Rule[uint64] { return All(rs...) })
			assertFlat(t, "All/"+strconv.Itoa(m), v, got, calls, allTrue, stopAt(bs, false))

			got, calls = evalFlat(m, v, func(rs []Rule[uint64]) Rule[uint64] { return Any(rs...) })
			assertFlat(t, "Any/"+strconv.Itoa(m), v, got, calls, anyTrue, stopAt(bs, true))

			got, calls = evalFlat(m, v, func(rs []Rule[uint64]) Rule[uint64] { return None(rs...) })
			assertFlat(t, "None/"+strconv.Itoa(m), v, got, calls, !anyTrue, stopAt(bs, true))

			for n := -1; n <= m+1; n++ {
				name := "AtLeast(" + strconv.Itoa(n) + ")/" + strconv.Itoa(m)
				got, calls = evalFlat(m, v, func(rs []Rule[uint64]) Rule[uint64] { return AtLeast(n, rs...) })
				assertFlat(t, name, v, got, calls, countTrue(bs) >= n, upTo(atLeastDecided(bs, n)))
				if n <= 0 && (!got || len(calls) != 0) {
					t.Fatalf("%s at input %b = %v after %v, want true without evaluating", name, v, got, calls)
				}
				if n > m && got {
					t.Fatalf("%s at input %b = true, want false when n exceeds the rule count", name, v)
				}
			}

			if m == 0 {
				continue
			}
			got, calls = evalFlat(m, v, func(rs []Rule[uint64]) Rule[uint64] { return rs[0].And(rs[1:]...) })
			assertFlat(t, "And/"+strconv.Itoa(m), v, got, calls, allTrue, stopAt(bs, false))

			got, calls = evalFlat(m, v, func(rs []Rule[uint64]) Rule[uint64] { return rs[0].Or(rs[1:]...) })
			assertFlat(t, "Or/"+strconv.Itoa(m), v, got, calls, anyTrue, stopAt(bs, true))

			switch m {
			case 1:
				got, calls = evalFlat(m, v, func(rs []Rule[uint64]) Rule[uint64] { return rs[0].Not() })
				assertFlat(t, "Not", v, got, calls, !bs[0], []int{0})
			case 2:
				got, calls = evalFlat(m, v, func(rs []Rule[uint64]) Rule[uint64] { return rs[0].Xor(rs[1]) })
				assertFlat(t, "Xor", v, got, calls, bs[0] != bs[1], []int{0, 1})

				wantCalls := []int{0}
				if bs[0] {
					wantCalls = []int{0, 1}
				}
				got, calls = evalFlat(m, v, func(rs []Rule[uint64]) Rule[uint64] { return rs[0].Requires(rs[1]) })
				assertFlat(t, "Requires", v, got, calls, !bs[0] || bs[1], wantCalls)
			}
		}
	}
}

// op is the combinator at a node of a generated rule tree.
type op int

const (
	opLeaf op = iota
	opNot
	opAnd
	opOr
	opXor
	opRequires
	opAll
	opAny
	opNone
	opAtLeast
)

// opCount is the number of ops.
const opCount = int(opAtLeast) + 1

// node is a generated rule tree. A leaf reads input bit bit and has a unique
// id. An AtLeast node passes when at least n of its kids pass.
type node struct {
	op   op
	id   int
	bit  int
	n    int
	kids []*node
}

// source supplies the choices that shape a generated tree. *rand.Rand
// satisfies it, and so does byteSource for fuzzing.
type source interface {
	IntN(n int) int
}

// generator builds rule trees from a source and numbers their leaves.
type generator struct {
	src    source
	leaves int
}

// tree returns a tree at most depth levels deep.
func (g *generator) tree(depth int) *node {
	o := op(g.src.IntN(opCount))
	if depth == 0 || o == opLeaf {
		leaf := &node{op: opLeaf, id: g.leaves, bit: g.src.IntN(leafBits)}
		g.leaves++
		return leaf
	}
	var arity int
	switch o {
	case opNot:
		arity = 1
	case opXor, opRequires:
		arity = 2
	case opAnd, opOr:
		arity = 1 + g.src.IntN(maxArity)
	default:
		arity = g.src.IntN(maxArity + 1)
	}
	nd := &node{op: o}
	for range arity {
		nd.kids = append(nd.kids, g.tree(depth-1))
	}
	if o == opAtLeast {
		nd.n = g.src.IntN(arity+3) - 1
	}
	return nd
}

// rule composes the tree with gate. Each leaf appends its id to trace when it
// runs, unless trace is nil.
func (n *node) rule(trace *[]int) Rule[uint64] {
	rules := make([]Rule[uint64], len(n.kids))
	for i, kid := range n.kids {
		rules[i] = kid.rule(trace)
	}
	switch n.op {
	case opLeaf:
		id, bit := n.id, n.bit
		return func(v uint64) bool {
			if trace != nil {
				*trace = append(*trace, id)
			}
			return bitSet(v, bit)
		}
	case opNot:
		return rules[0].Not()
	case opAnd:
		return rules[0].And(rules[1:]...)
	case opOr:
		return rules[0].Or(rules[1:]...)
	case opXor:
		return rules[0].Xor(rules[1])
	case opRequires:
		return rules[0].Requires(rules[1])
	case opAll:
		return All(rules...)
	case opAny:
		return Any(rules...)
	case opNone:
		return None(rules...)
	default: // opAtLeast
		return AtLeast(n.n, rules...)
	}
}

// eval is the reference evaluator. It computes the tree's value at v from the
// documented semantics, without gate, and appends the id of every leaf that a
// minimal left-to-right evaluation visits to trace.
func (n *node) eval(v uint64, trace *[]int) bool {
	switch n.op {
	case opLeaf:
		*trace = append(*trace, n.id)
		return bitSet(v, n.bit)
	case opNot:
		return !n.kids[0].eval(v, trace)
	case opAnd, opAll:
		for _, kid := range n.kids {
			if !kid.eval(v, trace) {
				return false
			}
		}
		return true
	case opOr, opAny, opNone:
		found := false
		for _, kid := range n.kids {
			if kid.eval(v, trace) {
				found = true
				break
			}
		}
		return found != (n.op == opNone)
	case opXor:
		left := n.kids[0].eval(v, trace)
		right := n.kids[1].eval(v, trace)
		return left != right
	case opRequires:
		if !n.kids[0].eval(v, trace) {
			return true
		}
		return n.kids[1].eval(v, trace)
	default: // opAtLeast
		passed := 0
		for i, kid := range n.kids {
			remaining := len(n.kids) - i
			if passed >= n.n || passed+remaining < n.n {
				break
			}
			if kid.eval(v, trace) {
				passed++
			}
		}
		return passed >= n.n
	}
}

// value returns the reference value of the tree at v.
func (n *node) value(v uint64) bool {
	var trace []int
	return n.eval(v, &trace)
}

// compose returns the tree composed with gate, without leaf tracing.
func (n *node) compose() Rule[uint64] {
	return n.rule(nil)
}

// String renders the tree for failure messages.
func (n *node) String() string {
	if n.op == opLeaf {
		return "b" + strconv.Itoa(n.bit)
	}
	names := [...]string{"leaf", "Not", "And", "Or", "Xor", "Requires", "All", "Any", "None", "AtLeast"}
	parts := make([]string, 0, len(n.kids)+1)
	if n.op == opAtLeast {
		parts = append(parts, strconv.Itoa(n.n))
	}
	for _, kid := range n.kids {
		parts = append(parts, kid.String())
	}
	return names[n.op] + "(" + strings.Join(parts, ", ") + ")"
}

// tracedTree is a tree composed once with gate, with leaf tracing, so it can
// be checked against the reference evaluator at many inputs.
type tracedTree struct {
	tree      *node
	rule      Rule[uint64]
	gotTrace  []int
	wantTrace []int
}

// newTracedTree composes tree with tracing.
func newTracedTree(tree *node) *tracedTree {
	tt := &tracedTree{tree: tree}
	tt.rule = tree.rule(&tt.gotTrace)
	return tt
}

// assertAt fails t when the composed tree's result or leaf evaluation order
// at v differs from the reference evaluator's.
func (tt *tracedTree) assertAt(t *testing.T, v uint64) {
	t.Helper()
	tt.gotTrace, tt.wantTrace = tt.gotTrace[:0], tt.wantTrace[:0]
	got := tt.rule(v)
	want := tt.tree.eval(v, &tt.wantTrace)
	if got != want {
		t.Fatalf("%v at input %b = %v, want %v", tt.tree, v, got, want)
	}
	if !slices.Equal(tt.gotTrace, tt.wantTrace) {
		t.Fatalf("%v at input %b evaluated leaves %v, want %v", tt.tree, v, tt.gotTrace, tt.wantTrace)
	}
}

// TestPropertyTreeEvaluation checks that composed trees agree with the
// reference evaluator on every result and evaluate exactly the leaves a
// minimal left-to-right evaluation needs, in order, at every nesting level.
func TestPropertyTreeEvaluation(t *testing.T) {
	r := newRand()
	g := &generator{src: r}
	for range propertyCases {
		tt := newTracedTree(g.tree(4))
		for range inputsPerCase {
			tt.assertAt(t, r.Uint64())
		}
	}
}

// law is an equation between two rules built from generated trees.
type law struct {
	name        string
	left, right func(a, b, c Rule[uint64]) Rule[uint64]
}

// TestPropertyLaws checks the algebraic laws of the combinators on random
// trees and inputs.
func TestPropertyLaws(t *testing.T) {
	laws := []law{
		{
			"De Morgan for And",
			func(a, b, _ Rule[uint64]) Rule[uint64] { return a.And(b).Not() },
			func(a, b, _ Rule[uint64]) Rule[uint64] { return a.Not().Or(b.Not()) },
		},
		{
			"De Morgan for Or",
			func(a, b, _ Rule[uint64]) Rule[uint64] { return a.Or(b).Not() },
			func(a, b, _ Rule[uint64]) Rule[uint64] { return a.Not().And(b.Not()) },
		},
		{
			"De Morgan for All",
			func(a, b, c Rule[uint64]) Rule[uint64] { return All(a, b, c).Not() },
			func(a, b, c Rule[uint64]) Rule[uint64] { return Any(a.Not(), b.Not(), c.Not()) },
		},
		{
			"De Morgan for Any",
			func(a, b, c Rule[uint64]) Rule[uint64] { return Any(a, b, c).Not() },
			func(a, b, c Rule[uint64]) Rule[uint64] { return All(a.Not(), b.Not(), c.Not()) },
		},
		{
			"double negation",
			func(a, _, _ Rule[uint64]) Rule[uint64] { return a.Not().Not() },
			func(a, _, _ Rule[uint64]) Rule[uint64] { return a },
		},
		{
			"And commutes",
			func(a, b, _ Rule[uint64]) Rule[uint64] { return a.And(b) },
			func(a, b, _ Rule[uint64]) Rule[uint64] { return b.And(a) },
		},
		{
			"Or commutes",
			func(a, b, _ Rule[uint64]) Rule[uint64] { return a.Or(b) },
			func(a, b, _ Rule[uint64]) Rule[uint64] { return b.Or(a) },
		},
		{
			"And associates",
			func(a, b, c Rule[uint64]) Rule[uint64] { return a.And(b).And(c) },
			func(a, b, c Rule[uint64]) Rule[uint64] { return a.And(b.And(c)) },
		},
		{
			"And flattens into All",
			func(a, b, c Rule[uint64]) Rule[uint64] { return a.And(b, c) },
			func(a, b, c Rule[uint64]) Rule[uint64] { return All(a, b, c) },
		},
		{
			"Or associates",
			func(a, b, c Rule[uint64]) Rule[uint64] { return a.Or(b).Or(c) },
			func(a, b, c Rule[uint64]) Rule[uint64] { return a.Or(b.Or(c)) },
		},
		{
			"Or flattens into Any",
			func(a, b, c Rule[uint64]) Rule[uint64] { return a.Or(b, c) },
			func(a, b, c Rule[uint64]) Rule[uint64] { return Any(a, b, c) },
		},
		{
			"All() is the identity of And",
			func(a, _, _ Rule[uint64]) Rule[uint64] { return a.And(All[uint64]()) },
			func(a, _, _ Rule[uint64]) Rule[uint64] { return a },
		},
		{
			"Any() is the identity of Or",
			func(a, _, _ Rule[uint64]) Rule[uint64] { return a.Or(Any[uint64]()) },
			func(a, _, _ Rule[uint64]) Rule[uint64] { return a },
		},
		{
			"Any() annihilates And",
			func(a, _, _ Rule[uint64]) Rule[uint64] { return a.And(Any[uint64]()) },
			func(_, _, _ Rule[uint64]) Rule[uint64] { return Any[uint64]() },
		},
		{
			"All() annihilates Or",
			func(a, _, _ Rule[uint64]) Rule[uint64] { return a.Or(All[uint64]()) },
			func(_, _, _ Rule[uint64]) Rule[uint64] { return All[uint64]() },
		},
		{
			"single-rule All and Any",
			func(a, _, _ Rule[uint64]) Rule[uint64] { return All(a).And(Any(a)) },
			func(a, _, _ Rule[uint64]) Rule[uint64] { return a },
		},
		{
			"Xor is inequality",
			func(a, b, _ Rule[uint64]) Rule[uint64] { return a.Xor(b) },
			func(a, b, _ Rule[uint64]) Rule[uint64] { return func(v uint64) bool { return a(v) != b(v) } },
		},
		{
			"Xor commutes",
			func(a, b, _ Rule[uint64]) Rule[uint64] { return a.Xor(b) },
			func(a, b, _ Rule[uint64]) Rule[uint64] { return b.Xor(a) },
		},
		{
			"Requires is implication from receiver to argument",
			func(a, b, _ Rule[uint64]) Rule[uint64] { return a.Requires(b) },
			func(a, b, _ Rule[uint64]) Rule[uint64] { return a.Not().Or(b) },
		},
		{
			"Requires fails only when the receiver passes and the argument fails",
			func(a, b, _ Rule[uint64]) Rule[uint64] { return a.Requires(b).Not() },
			func(a, b, _ Rule[uint64]) Rule[uint64] { return a.And(b.Not()) },
		},
		{
			"None is Not Any",
			func(a, b, c Rule[uint64]) Rule[uint64] { return None(a, b, c) },
			func(a, b, c Rule[uint64]) Rule[uint64] { return Any(a, b, c).Not() },
		},
		{
			"None is All of Not",
			func(a, b, c Rule[uint64]) Rule[uint64] { return None(a, b, c) },
			func(a, b, c Rule[uint64]) Rule[uint64] { return All(a.Not(), b.Not(), c.Not()) },
		},
		{
			"AtLeast(1) is Any",
			func(a, b, c Rule[uint64]) Rule[uint64] { return AtLeast(1, a, b, c) },
			func(a, b, c Rule[uint64]) Rule[uint64] { return Any(a, b, c) },
		},
		{
			"AtLeast(len) is All",
			func(a, b, c Rule[uint64]) Rule[uint64] { return AtLeast(3, a, b, c) },
			func(a, b, c Rule[uint64]) Rule[uint64] { return All(a, b, c) },
		},
		{
			"AtLeast(2) is majority",
			func(a, b, c Rule[uint64]) Rule[uint64] { return AtLeast(2, a, b, c) },
			func(a, b, c Rule[uint64]) Rule[uint64] { return Any(a.And(b), a.And(c), b.And(c)) },
		},
	}

	r := newRand()
	g := &generator{src: r}
	for range propertyCases {
		ta, tb, tc := g.tree(3), g.tree(3), g.tree(3)
		a, b, c := ta.compose(), tb.compose(), tc.compose()
		lefts := make([]Rule[uint64], len(laws))
		rights := make([]Rule[uint64], len(laws))
		for i, l := range laws {
			lefts[i], rights[i] = l.left(a, b, c), l.right(a, b, c)
		}
		for range inputsPerCase {
			v := r.Uint64()
			if All[uint64]()(v) != true || Any[uint64]()(v) != false || None[uint64]()(v) != true {
				t.Fatalf("empty All, Any, None at input %b = %v, %v, %v, want true, false, true",
					v, All[uint64]()(v), Any[uint64]()(v), None[uint64]()(v))
			}
			for i, l := range laws {
				if left, right := lefts[i](v), rights[i](v); left != right {
					t.Fatalf("%s fails at input %b with a = %v, b = %v, c = %v: left %v, right %v",
						l.name, v, ta, tb, tc, left, right)
				}
			}
		}
	}
}

// TestPropertyArgumentCopying checks that rewriting the caller's slice after
// building a combinator does not change the combinator.
func TestPropertyArgumentCopying(t *testing.T) {
	r := newRand()
	g := &generator{src: r}
	for range propertyCases / 10 {
		trees := make([]*node, 1+r.IntN(maxArity))
		rules := make([]Rule[uint64], len(trees))
		checks := make([]Check[uint64], len(trees))
		errs := make([]error, len(trees))
		for i := range trees {
			trees[i] = g.tree(3)
			rules[i] = trees[i].compose()
			errs[i] = errors.New("tree " + strconv.Itoa(i))
			checks[i] = rules[i].Else(errs[i])
		}
		n := r.IntN(len(rules)+3) - 1
		built := map[string]Rule[uint64]{
			"All":     All(rules...),
			"Any":     Any(rules...),
			"None":    None(rules...),
			"AtLeast": AtLeast(n, rules...),
			"And":     rules[0].And(rules[1:]...),
			"Or":      rules[0].Or(rules[1:]...),
		}
		join, first := Join(checks...), First(checks...)

		inputs := make([]uint64, inputsPerCase)
		before := map[string][]bool{}
		var joinBefore, firstBefore []error
		for i := range inputs {
			inputs[i] = r.Uint64()
			for name, rule := range built {
				before[name] = append(before[name], rule(inputs[i]))
			}
			joinBefore = append(joinBefore, join(inputs[i]))
			firstBefore = append(firstBefore, first(inputs[i]))
		}

		for i := range rules {
			rules[i] = rules[i].Not()
			checks[i] = func(uint64) error { return errs[i] }
		}

		for i, v := range inputs {
			for name, rule := range built {
				if got := rule(v); got != before[name][i] {
					t.Fatalf("%s over %v at input %b changed from %v to %v after the slice was rewritten",
						name, trees, v, before[name][i], got)
				}
			}
			if got := join(v); (got == nil) != (joinBefore[i] == nil) || (got != nil && got.Error() != joinBefore[i].Error()) {
				t.Fatalf("Join at input %b changed from %v to %v after the slice was rewritten", v, joinBefore[i], got)
			}
			if got := first(v); got != firstBefore[i] {
				t.Fatalf("First at input %b changed from %v to %v after the slice was rewritten", v, firstBefore[i], got)
			}
		}
	}
}

// recordingChecks returns m checks where check i records i in calls and
// fails with errs[i] when bit i of its input is set.
func recordingChecks(calls *[]int, errs []error) []Check[uint64] {
	checks := make([]Check[uint64], len(errs))
	for i, err := range errs {
		checks[i] = func(v uint64) error {
			*calls = append(*calls, i)
			if bitSet(v, i) {
				return err
			}
			return nil
		}
	}
	return checks
}

// TestPropertyChecks checks First and Join against every pass and fail
// assignment of up to five checks.
func TestPropertyChecks(t *testing.T) {
	for m := range 6 {
		errs := make([]error, m)
		for i := range errs {
			errs[i] = errors.New("check " + strconv.Itoa(i))
		}
		for v := range uint64(1) << m {
			bs := bools(v, m)

			var calls []int
			err := First(recordingChecks(&calls, errs)...)(v)
			wantCalls := stopAt(bs, true)
			if i := slices.Index(bs, true); i >= 0 {
				if err != errs[i] {
					t.Fatalf("First at input %b = %v, want exactly %v", v, err, errs[i])
				}
			} else if err != nil {
				t.Fatalf("First at input %b = %v, want nil", v, err)
			}
			if !slices.Equal(calls, wantCalls) {
				t.Fatalf("First at input %b ran checks %v, want %v", v, calls, wantCalls)
			}

			calls = nil
			err = Join(recordingChecks(&calls, errs)...)(v)
			if !slices.Equal(calls, upTo(m)) {
				t.Fatalf("Join at input %b ran checks %v, want %v", v, calls, upTo(m))
			}
			if (err == nil) != (v == 0) {
				t.Fatalf("Join at input %b = %v, want nil only when every check passes", v, err)
			}
			var wantErrs []error
			for i, failed := range bs {
				if errors.Is(err, errs[i]) != failed {
					t.Fatalf("Join at input %b: errors.Is(err, %v) = %v, want %v", v, errs[i], !failed, failed)
				}
				if failed {
					wantErrs = append(wantErrs, errs[i])
				}
			}
			var joined interface{ Unwrap() []error }
			if err != nil && (!errors.As(err, &joined) || !slices.Equal(joined.Unwrap(), wantErrs)) {
				t.Fatalf("Join at input %b = %v, want the failing errors %v in order", v, err, wantErrs)
			}
		}
	}
}

// TestPropertyElse checks that Else reports its error exactly when the rule
// fails, alone and inside First and Join, on random trees.
func TestPropertyElse(t *testing.T) {
	r := newRand()
	g := &generator{src: r}
	errFailed := errors.New("rule failed")
	for range propertyCases {
		tree := g.tree(4)
		check := tree.compose().Else(errFailed)
		for range inputsPerCase {
			v := r.Uint64()
			err := check(v)
			if tree.value(v) {
				if err != nil {
					t.Fatalf("Else of %v at input %b = %v, want nil when the rule passes", tree, v, err)
				}
			} else if err != errFailed {
				t.Fatalf("Else of %v at input %b = %v, want exactly %v when the rule fails", tree, v, err, errFailed)
			}
		}
	}

	for range propertyCases / 10 {
		trees := make([]*node, r.IntN(maxArity+1))
		checks := make([]Check[uint64], len(trees))
		errs := make([]error, len(trees))
		for i := range trees {
			trees[i] = g.tree(3)
			errs[i] = errors.New("tree " + strconv.Itoa(i))
			checks[i] = trees[i].compose().Else(errs[i])
		}
		first, join := First(checks...), Join(checks...)
		for range inputsPerCase {
			v := r.Uint64()
			var wantFirst error
			for i, tree := range trees {
				failed := !tree.value(v)
				if failed && wantFirst == nil {
					wantFirst = errs[i]
				}
				if got := errors.Is(join(v), errs[i]); got != failed {
					t.Fatalf("Join over %v at input %b: errors.Is(err, %v) = %v, want %v", trees, v, errs[i], got, failed)
				}
			}
			if got := first(v); got != wantFirst {
				t.Fatalf("First over %v at input %b = %v, want %v", trees, v, got, wantFirst)
			}
		}
	}
}
