// Package gate provides typed, composable boolean rules and the checks that
// explain why a rule failed.
//
// A [Rule] is a plain func(T) bool over any value the caller chooses: a
// request, a configuration, an order, or a small struct that pairs a subject
// with the thing it acts on. Rules combine with [Rule.And], [Rule.Or],
// [Rule.Not], [Rule.Xor], and [Rule.Requires], or with [All], [Any], [None],
// and [AtLeast]. Because a rule is a function, any func(T) bool literal is a
// rule and a rule is called directly:
//
//	type rule = gate.Rule[Access]
//
//	var (
//		isAdmin rule = func(a Access) bool { return a.User.Admin }
//		isOwner rule = func(a Access) bool { return a.User.ID == a.Doc.OwnerID }
//		canEdit      = isAdmin.Or(isOwner)
//	)
//
//	if canEdit(Access{User: user, Doc: doc}) { ... }
//
// A [Check] is a func(T) error. [Rule.Else] turns a rule into a check that
// reports a caller-supplied error when the rule fails; [Join] reports every
// failing check and [First] reports only the first.
//
// The package owns composition and evaluation order. Callers own the value
// type, every leaf rule, and every error a check reports; gate defines no
// errors of its own.
//
// Combinators that can decide early stop evaluating as soon as the result is
// known. Combinators copy their rule and check arguments when they are built,
// so later changes to a caller's slice do not affect them. Calling a nil Rule
// or Check panics like any nil function call; callers must not pass nil.
//
// Rules and checks are immutable after construction and safe for concurrent
// use when the leaf functions they wrap are. Evaluating a composed rule does
// not allocate.
package gate
