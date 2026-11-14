package gate_test

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/brian-nunez/bkit/gate"
)

type User struct {
	ID     string
	Roles  []string
	Banned bool
}

type Doc struct {
	OwnerID string
	Editors []string
	Draft   bool
}

// Access pairs a user with the document they act on.
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
)

func Example() {
	canDelete := hasRole("admin").Or(hasRole("user").And(isOwner))
	canView := hasRole("admin").Or(gate.All(
		isDraft.Requires(isOwner.Or(isEditor)),
		gate.None(isBanned),
	))

	doc := Doc{OwnerID: "u1", Editors: []string{"u2"}, Draft: true}
	owner := User{ID: "u1", Roles: []string{"user"}}
	editor := User{ID: "u2", Roles: []string{"user"}}
	admin := User{ID: "u9", Roles: []string{"admin"}}

	fmt.Println(canDelete(Access{User: owner, Doc: doc}))
	fmt.Println(canDelete(Access{User: editor, Doc: doc}))
	fmt.Println(canDelete(Access{User: admin, Doc: doc}))
	fmt.Println(canView(Access{User: editor, Doc: doc}))
	fmt.Println(canView(Access{User: User{ID: "u3"}, Doc: doc}))
	// Output:
	// true
	// false
	// true
	// true
	// false
}

type Post struct {
	Body       string
	Reputation int
}

func ExampleRule_Requires() {
	type postRule = gate.Rule[Post]

	var (
		hasLinks postRule = func(p Post) bool { return strings.Contains(p.Body, "http") }
		trusted  postRule = func(p Post) bool { return p.Reputation >= 100 }
	)

	// Only trusted authors may post links.
	canPublish := hasLinks.Requires(trusted)

	fmt.Println(canPublish(Post{Body: "hello", Reputation: 1}))
	fmt.Println(canPublish(Post{Body: "see http://example.com", Reputation: 1}))
	fmt.Println(canPublish(Post{Body: "see http://example.com", Reputation: 500}))
	// Output:
	// true
	// false
	// true
}

type SignIn struct {
	KnownDevice bool
	UsualRegion bool
	RecentLogin bool
	PassedMFA   bool
}

func ExampleAtLeast() {
	type signInRule = gate.Rule[SignIn]

	var (
		knownDevice signInRule = func(s SignIn) bool { return s.KnownDevice }
		usualRegion signInRule = func(s SignIn) bool { return s.UsualRegion }
		recentLogin signInRule = func(s SignIn) bool { return s.RecentLogin }
		passedMFA   signInRule = func(s SignIn) bool { return s.PassedMFA }
	)

	canSignIn := gate.AtLeast(2, knownDevice, usualRegion, recentLogin).Or(passedMFA)

	fmt.Println(canSignIn(SignIn{KnownDevice: true, RecentLogin: true}))
	fmt.Println(canSignIn(SignIn{KnownDevice: true}))
	fmt.Println(canSignIn(SignIn{KnownDevice: true, PassedMFA: true}))
	// Output:
	// true
	// false
	// true
}

func ExampleRule_Filter() {
	docs := []Doc{
		{OwnerID: "u1", Draft: true},
		{OwnerID: "u2", Draft: true},
		{OwnerID: "u2", Draft: false},
	}
	user := User{ID: "u1"}

	canView := isDraft.Requires(isOwner)

	accesses := func(yield func(Access) bool) {
		for _, d := range docs {
			if !yield(Access{User: user, Doc: d}) {
				return
			}
		}
	}
	for a := range canView.Filter(accesses) {
		fmt.Println(a.Doc.OwnerID, a.Doc.Draft)
	}
	// Output:
	// u1 true
	// u2 false
}

type Config struct {
	Port     int
	Password string
	CertFile string
	TLS      bool
}

var (
	ErrBadPort      = errors.New("port must be 1-65535")
	ErrAuthMethod   = errors.New("set exactly one of password or cert file")
	ErrCertNeedsTLS = errors.New("cert file requires tls")
)

func ExampleJoin() {
	type cfgRule = gate.Rule[Config]

	var (
		hasPort     cfgRule = func(c Config) bool { return c.Port > 0 && c.Port < 65536 }
		hasPassword cfgRule = func(c Config) bool { return c.Password != "" }
		hasCertFile cfgRule = func(c Config) bool { return c.CertFile != "" }
		usesTLS     cfgRule = func(c Config) bool { return c.TLS }
	)

	validate := gate.Join(
		hasPort.Else(ErrBadPort),
		hasPassword.Xor(hasCertFile).Else(ErrAuthMethod),
		hasCertFile.Requires(usesTLS).Else(ErrCertNeedsTLS),
	)

	err := validate(Config{Port: 0, Password: "x", CertFile: "cert.pem"})
	fmt.Println(err)
	fmt.Println(errors.Is(err, ErrAuthMethod))
	fmt.Println(validate(Config{Port: 8443, CertFile: "cert.pem", TLS: true}))
	// Output:
	// port must be 1-65535
	// set exactly one of password or cert file
	// cert file requires tls
	// true
	// <nil>
}

var (
	ErrBanned   = errors.New("account is banned")
	ErrNotOwner = errors.New("only the owner can delete this")
)

func ExampleFirst() {
	checkDelete := gate.First(
		isBanned.Not().Else(ErrBanned),
		hasRole("admin").Or(isOwner).Else(ErrNotOwner),
	)

	doc := Doc{OwnerID: "u1"}

	fmt.Println(checkDelete(Access{User: User{ID: "u1"}, Doc: doc}))
	fmt.Println(checkDelete(Access{User: User{ID: "u2"}, Doc: doc}))
	fmt.Println(checkDelete(Access{User: User{ID: "u2", Banned: true}, Doc: doc}))
	// Output:
	// <nil>
	// only the owner can delete this
	// account is banned
}
