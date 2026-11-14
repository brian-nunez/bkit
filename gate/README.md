# gate

Small, typed rules you can combine, plus checks that tell you which rule failed.

A rule is a `func(T) bool` over any type. gate combines rules with `And`,
`Or`, `Not`, and friends, and turns them into checks that return errors. It
only uses the standard library.

## Install

```shell
go get github.com/brian-nunez/bkit/gate
```

```go
import "github.com/brian-nunez/bkit/gate"
```

## Rules

Any `func(T) bool` is a rule, and you call a rule like a function. A type
alias keeps the definitions short.

When a rule needs two things, like a user and the document they want to
touch, put them in a struct:

```go
type Access struct {
	User User
	Doc  Doc
}

type rule = gate.Rule[Access]

func hasRole(role string) rule {
	return func(a Access) bool { return slices.Contains(a.User.Roles, role) }
}

var (
	isOwner  rule = func(a Access) bool { return a.User.ID == a.Doc.OwnerID }
	isEditor rule = func(a Access) bool { return slices.Contains(a.Doc.Editors, a.User.ID) }
	isDraft  rule = func(a Access) bool { return a.Doc.Draft }
	isBanned rule = func(a Access) bool { return a.User.Banned }

	canDelete = hasRole("admin").Or(hasRole("user").And(isOwner))
	canView   = hasRole("admin").Or(gate.All(
		isDraft.Requires(isOwner.Or(isEditor)),
		gate.None(isBanned),
	))
)

if !canDelete(Access{User: user, Doc: doc}) {
	return ErrForbidden
}
```

The same pattern works for orders:

```go
type orderRule = gate.Rule[Order]

var (
	inStock   orderRule = func(o Order) bool { return o.Stock > 0 }
	notOnHold orderRule = func(o Order) bool { return !o.Hold }
	canShip             = inStock.And(notOnHold)
)
```

| Function | Passes when |
| --- | --- |
| `r.And(rules...)` | `r` and all the other rules pass |
| `r.Or(rules...)` | `r` or any of the other rules pass |
| `r.Not()` | `r` fails |
| `r.Xor(other)` | exactly one of `r` and `other` passes |
| `r.Requires(then)` | `r` fails, or both pass |
| `gate.All(rules...)` | every rule passes (true when empty) |
| `gate.Any(rules...)` | at least one rule passes (false when empty) |
| `gate.None(rules...)` | no rule passes (true when empty) |
| `gate.AtLeast(n, rules...)` | at least `n` rules pass (true when `n <= 0`) |

Rules run left to right and stop once the answer is known. `Xor` always
runs both sides.

`r.Filter(seq)` gives back only the values from an `iter.Seq[T]` that pass:

```go
for o := range canShip.Filter(slices.Values(orders)) {
	ship(o)
}
```

## Checks

A check is a `func(T) error`. `r.Else(err)` returns `err` when `r` fails.

Use `gate.Join` to get every failure at once, which works well for config
and forms. Use `gate.First` to stop at the first failure.

```go
var (
	ErrBadPort    = errors.New("port must be 1-65535")
	ErrAuthMethod = errors.New("set exactly one of password or cert file")
)

validate := gate.Join(
	hasPort.Else(ErrBadPort),
	hasPassword.Xor(hasCertFile).Else(ErrAuthMethod),
)

err := validate(cfg)
if errors.Is(err, ErrAuthMethod) {
	// ...
}
```

You can also write a check by hand when you want a custom message:

```go
validPost := gate.First(
	hasBody.Else(ErrEmptyBody),
	func(p Post) error {
		if len(p.Body) > 5000 {
			return fmt.Errorf("%w: %d characters", ErrTooLong, len(p.Body))
		}
		return nil
	},
)
```

## Good to know

- Rules and checks copy the slices you pass in. Changing the slice later
  won't change the rule.
- Don't pass a nil rule or check. Calling one panics, the same as any nil
  func.
- `Else(nil)` never fails.
- Rules and checks never change after they are built. They are safe to use
  from many goroutines when the functions inside them are.
- Running a rule doesn't allocate.

## Errors

Checks return the errors passed to `Else`, or the ones a hand-written check
returns. `Join` combines them with `errors.Join`. `errors.Is` and `errors.As`
work on the result.

## Development

From the repository root:

```shell
make -C gate check
```

This runs the format check, lint, vet, tests with the race detector, and a
coverage check that requires 100%.

## Releases

Each release gets a tag like `gate/vX.Y.Z`:

```shell
go get github.com/brian-nunez/bkit/gate@vX.Y.Z
```
