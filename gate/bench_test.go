package gate

import "testing"

var benchResult bool

func BenchmarkComposedRule(b *testing.B) {
	isAdmin := Rule[int](func(v int) bool { return v == 1 })
	isUser := Rule[int](func(v int) bool { return v >= 2 })
	isOwner := Rule[int](func(v int) bool { return v%2 == 0 })

	canDelete := All(
		isAdmin.Or(isUser.And(isOwner)),
		None(isAdmin.Not().And(isOwner.Not())),
		isUser.Requires(isOwner),
		AtLeast(2, isAdmin, isUser, isOwner),
		isAdmin.Xor(isUser),
	)

	b.ReportAllocs()
	for i := 0; b.Loop(); i++ {
		benchResult = canDelete(i)
	}
}

var benchErr error

func BenchmarkPassingChecks(b *testing.B) {
	check := Join(
		pass.Else(errA),
		even.Or(even.Not()).Else(errB),
	)

	b.ReportAllocs()
	for i := 0; b.Loop(); i++ {
		benchErr = check(i)
	}
}
