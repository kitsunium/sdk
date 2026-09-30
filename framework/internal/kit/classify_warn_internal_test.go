package kit

import (
	"context"
	"net"
	"net/netip"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// lookalike's names read like personal data, some on a type that cannot
// hold what they mention.
type lookalike struct {
	Email           string     `json:"email"`
	FirstName       string     `json:"first_name"`
	ClientIP        string     `json:"clientIp"`
	IBAN            string     `json:"iban" kit:"public"`
	Phone           string     `json:"phone" kit:"personal"`
	Zip             string     `json:"zip"`
	Tip             string     `json:"tip"`
	RequireEmail    bool       `json:"requireEmail"`
	EmailVerifiedAt *time.Time `json:"emailVerifiedAt,omitempty"`
	BirthDate       time.Time  `json:"birthDate"`
	ZipCode         int        `json:"zipCode"`
}

// warningsOf is what the plan of typ warns of, in English.
func warningsOf(typ reflect.Type) []string {
	var said []string
	for _, w := range planOf(typ).warnings {
		said = append(said, w.said.String())
	}
	return said
}

// looksPersonalSaid is the warning of a field named label.
func looksPersonalSaid(label string) string {
	return say("classify.looks-personal", "field", label).String()
}

// A field whose name and type read like personal data but has no class is
// warned of; a class silences it, public included, and so does a type that
// cannot hold what the name mentions.
func TestTheHeuristicWarns(t *testing.T) {
	var want []string
	for _, field := range []string{"Email", "FirstName", "ClientIP", "BirthDate", "ZipCode"} {
		want = append(want, looksPersonalSaid("kit.lookalike."+field))
	}
	if said := warningsOf(reflect.TypeFor[lookalike]()); !slices.Equal(said, want) {
		t.Errorf("warned of\n%s\nwant\n%s", strings.Join(said, "\n"), strings.Join(want, "\n"))
	}
}

// The types the heuristic reads.
type (
	// wireText is a named string, as a product's own types are.
	wireText string
	// wireSwitch is a named bool.
	wireSwitch bool
	// yesNo is a bool that writes itself as text: it still holds one bit.
	yesNo bool
	// optIns are switches, and nothing else.
	optIns struct{ Weekly, Daily bool }
	// postal is an address, as a struct holds one.
	postal struct{ Street, City string }
	// linkedName is a name in parts, a type that reaches itself.
	linkedName struct {
		Part string
		Next *linkedName
	}
	// civilDate is a day, in numbers.
	civilDate struct{ Year, Month, Day int }
)

// MarshalText writes yes or no.
func (y yesNo) MarshalText() ([]byte, error) {
	if y {
		return []byte("yes"), nil
	}
	return []byte("no"), nil
}

// A field is warned of only when its type can hold what its name mentions,
// whatever the JSON it writes: per type, the names it is and is not warned
// of under.
func TestTheHeuristicReadsTheType(t *testing.T) {
	for _, c := range []struct {
		name string
		typ  reflect.Type
		want bool
	}{
		// A bool holds none of it: named, written as text, through a
		// pointer, in a list or in a struct of switches.
		{"RequireEmail", reflect.TypeFor[bool](), false},
		{"RequireEmail", reflect.TypeFor[*bool](), false},
		{"EmailVerified", reflect.TypeFor[wireSwitch](), false},
		{"EmailVerified", reflect.TypeFor[yesNo](), false},
		{"PhoneChecks", reflect.TypeFor[[]bool](), false},
		{"EmailOptIns", reflect.TypeFor[optIns](), false},
		// Text holds all of it: a string, a named one, bytes, an interface,
		// and a list, a map — its keys too — or a struct that holds text.
		{"Email", reflect.TypeFor[string](), true},
		{"email", reflect.TypeFor[*wireText](), true},
		{"LastName", reflect.TypeFor[[]byte](), true},
		{"IBAN", reflect.TypeFor[any](), true},
		{"Emails", reflect.TypeFor[[]string](), true},
		{"Emails", reflect.TypeFor[map[string]bool](), true},
		{"Address", reflect.TypeFor[postal](), true},
		{"FullName", reflect.TypeFor[linkedName](), true},
		// An instant holds a birth date, and nothing else a name mentions.
		{"BirthDate", reflect.TypeFor[time.Time](), true},
		{"birth_date", reflect.TypeFor[*time.Time](), true},
		{"EmailVerifiedAt", reflect.TypeFor[time.Time](), false},
		{"AddressChangedAt", reflect.TypeFor[*time.Time](), false},
		// A number holds a birth, an IP and what is written with digits: a
		// phone, a postcode, a tax number — never an e-mail, a name, a
		// street, an IBAN or an address.
		{"ZipCode", reflect.TypeFor[int](), true},
		{"Phone", reflect.TypeFor[int64](), true},
		{"TaxID", reflect.TypeFor[uint64](), true},
		{"BirthYear", reflect.TypeFor[uint16](), true},
		{"DateOfBirth", reflect.TypeFor[civilDate](), true},
		{"ClientIP", reflect.TypeFor[uint32](), true},
		{"EmailsSent", reflect.TypeFor[int](), false},
		{"LastNameLength", reflect.TypeFor[int](), false},
		{"StreetNumber", reflect.TypeFor[int](), false},
		{"IBANScore", reflect.TypeFor[float64](), false},
		{"AddressID", reflect.TypeFor[int64](), false},
		// An IP type holds what a field named after an IP or an address
		// holds, and nothing else.
		{"IP", reflect.TypeFor[net.IP](), true},
		{"IP", reflect.TypeFor[[4]byte](), true},
		{"clientIp", reflect.TypeFor[netip.Addr](), true},
		{"RemoteAddr", reflect.TypeFor[netip.AddrPort](), true},
		{"Address", reflect.TypeFor[*netip.Addr](), true},
		{"Email", reflect.TypeFor[netip.Addr](), false},
		{"Phone", reflect.TypeFor[net.IP](), false},
		// A function and a channel hold nothing.
		{"EmailSender", reflect.TypeFor[func() string](), false},
		{"Emails", reflect.TypeFor[chan string](), false},
		// A name that mentions nothing personal is never warned of.
		{"Title", reflect.TypeFor[string](), false},
	} {
		if got := looksPersonal(c.typ, c.name); got != c.want {
			t.Errorf("%s %s: warned %v, want %v", c.name, c.typ, got, c.want)
		}
	}
}

// onceRef declares the field every type below reaches: the address a
// member is known by.
type onceRef struct {
	ID    string `json:"id"`
	Email string `json:"email"`
}

// onceUser embeds it, as a member sees their own account.
type onceUser struct {
	onceRef
	Locale string `json:"locale"`
}

// oncePerson embeds the account through a pointer, as the other services
// see a member: the address is promoted two levels up.
type oncePerson struct {
	*onceUser
	Since time.Time `json:"since"`
}

// A field promoted into several types is one field: each plan names the
// struct that declares it, however deep the embedding, and an app whose
// store, endpoint and topic reach it through three types warns of it once,
// at the first declaration.
func TestAPromotedFieldIsWarnedOfOnce(t *testing.T) {
	said := looksPersonalSaid("kit.onceRef.Email")
	for _, typ := range []reflect.Type{reflect.TypeFor[onceRef](), reflect.TypeFor[onceUser](), reflect.TypeFor[oncePerson]()} {
		if got := warningsOf(typ); !slices.Equal(got, []string{said}) {
			t.Errorf("%s warns of %q", typ, got)
		}
	}
	svc := NewService("warned-once", "")
	svc.Store("users", func(u onceUser) string { return u.ID })
	svc.Endpoint("POST /people", func(context.Context, EmptyValue) (oncePerson, error) { return oncePerson{}, nil })
	svc.Topic[onceRef]("refs")
	var warned []string
	for _, d := range NewApp("once", svc).classificationProblems(true) {
		warned = append(warned, d.Node+": "+d.Message)
	}
	if want := []string{"warned-once/store/users: " + said}; !slices.Equal(warned, want) {
		t.Errorf("warned of\n%s\nwant\n%s", strings.Join(warned, "\n"), strings.Join(want, "\n"))
	}
}

// twoAlike holds two declared fields that print alike, each in a struct
// with no name.
type twoAlike struct {
	Home struct{ Email string } `json:"home"`
	Work struct {
		Email string
		Desk  int
	} `json:"work"`
}

// Two fields that print alike are still two fields: each is warned of.
func TestFieldsThatPrintAlikeAreWarnedOfApart(t *testing.T) {
	said := looksPersonalSaid("Email")
	if got := warningsOf(reflect.TypeFor[twoAlike]()); !slices.Equal(got, []string{said, said}) {
		t.Errorf("warns of %q", got)
	}
}

// secretive's fields are tagged secret: credentials, and personal data put
// under the one word kit once had to hide a value.
type secretive struct {
	ID           string `json:"id"`
	Email        string `json:"email" kit:"secret"`
	PostalAddr   string `json:"postalAddress" kit:"secret"`
	EmailHash    string `json:"emailHash" kit:"secret"`
	PasswordHash string `json:"passwordHash" kit:"secret"`
	PhoneToken   string `json:"phoneToken" kit:"secret"`
	APIKey       string `json:"apiKey" kit:"secret"`
	Mobile       bool   `json:"mobile" kit:"secret"`
	Phone        string `json:"phone" kit:"personal"`
}

// secretPersonalSaid is the warning of a secret field named label.
func secretPersonalSaid(label string) string {
	return say("classify.secret-personal", "field", label).String()
}

// A field tagged secret whose name reads like personal data — and not like
// a credential: a password, a token, a key, a hash — is warned of: secret
// means a credential since ADR 0006, which kit never exports. The same
// heuristic reads its type: a switch is never warned of.
func TestASecretThatReadsLikePersonalDataIsWarnedOf(t *testing.T) {
	want := []string{secretPersonalSaid("kit.secretive.Email"), secretPersonalSaid("kit.secretive.PostalAddr")}
	if said := warningsOf(reflect.TypeFor[secretive]()); !slices.Equal(said, want) {
		t.Errorf("warned of\n%s\nwant\n%s", strings.Join(said, "\n"), strings.Join(want, "\n"))
	}
	for _, c := range []struct {
		names []string
		want  bool
	}{
		{[]string{"email"}, true},
		{[]string{"firstName", "FirstName"}, true},
		{[]string{"emailHash", "EmailHash"}, false},
		{[]string{"email", "EmailDigest"}, false},
		{[]string{"resetToken"}, false},
		{[]string{"addressKey"}, false},
		{[]string{"password"}, false},
		{[]string{"organisation"}, false},
	} {
		if got := secretLooksPersonal(reflect.TypeFor[string](), c.names...); got != c.want {
			t.Errorf("%v: warned %v, want %v", c.names, got, c.want)
		}
	}
	// The app says it where it says the heuristic's other warnings: in dev,
	// in config and privacy — when kit explains —, at the declaration.
	svc := NewService("secretive", "")
	svc.Store("people", func(p secretive) string { return p.ID })
	var warned []string
	for _, d := range NewApp("secretive", svc).classificationProblems(true) {
		warned = append(warned, d.Severity+" "+d.Node+": "+d.Message)
	}
	if !slices.Contains(warned, "warning secretive/store/people: "+want[0]) || len(warned) != 2 {
		t.Errorf("the app warned of\n%s", strings.Join(warned, "\n"))
	}
	if quiet := NewApp("secretive", svc).classificationProblems(false); len(quiet) != 0 {
		t.Errorf("a start that does not explain warned of %+v", quiet)
	}
}
