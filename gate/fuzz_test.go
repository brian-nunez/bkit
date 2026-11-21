package gate

import "testing"

// byteSource is a source that reads its choices from a fuzzer's bytes. Once
// the bytes run out every choice is 0, which makes a leaf, so every byte
// string decodes to a finite tree.
type byteSource struct {
	data []byte
}

// IntN returns the next byte modulo n, or 0 when the bytes are spent.
func (s *byteSource) IntN(n int) int {
	if len(s.data) == 0 {
		return 0
	}
	b := s.data[0]
	s.data = s.data[1:]
	return int(b) % n
}

// FuzzRuleTree decodes a byte string into a rule tree and checks that the
// tree composed with gate agrees with the reference evaluator, in result and
// in leaf evaluation order, and that its negation and Else check agree too.
func FuzzRuleTree(f *testing.F) {
	f.Add([]byte{}, uint64(0))
	f.Add([]byte{byte(opNot), byte(opLeaf), 3}, uint64(0b1000))
	f.Add([]byte{byte(opAtLeast), 3, byte(opLeaf), 0, byte(opLeaf), 1, byte(opLeaf), 2, 4}, uint64(0b011))
	f.Add([]byte{byte(opRequires), byte(opAny), 2, byte(opLeaf), 0, byte(opLeaf), 1, byte(opXor), byte(opLeaf), 2, byte(opLeaf), 3}, uint64(0b0110))
	f.Add([]byte{byte(opNone), 4, byte(opAnd), 1, byte(opLeaf), 5, byte(opOr), 0, byte(opLeaf), 4, byte(opAll), 0, byte(opAtLeast), 0, 0}, uint64(0b110000))
	f.Fuzz(func(t *testing.T, data []byte, v uint64) {
		g := &generator{src: &byteSource{data: data}}
		tree := g.tree(5)
		newTracedTree(tree).assertAt(t, v)
		rule := tree.compose()
		if rule.Not()(v) == rule(v) {
			t.Fatalf("Not of %v at input %b equals the rule", tree, v)
		}
		if err := rule.Else(errA)(v); (err == nil) != tree.value(v) {
			t.Fatalf("Else of %v at input %b = %v, want an error exactly when the rule fails", tree, v, err)
		}
	})
}
