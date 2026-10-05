package kit

import (
	"net"
	"net/netip"
	"reflect"
	"slices"
	"strings"
)

const (
	// holdsText is text of any length: a string, bytes.
	holdsText holding = 1 << iota
	// holdsNumber is a number, an integer or not.
	holdsNumber
	// holdsTime is an instant: a time.Time.
	holdsTime
	// holdsIP is an IP address: net.IP, netip.Addr, netip.AddrPort,
	// netip.Prefix.
	holdsIP
)

const (
	// holdsAnything is what an interface holds.
	holdsAnything = holdsText | holdsNumber | holdsTime | holdsIP
	// ipHeldBy is what holds an IP address: text, a number — an IPv4 fits
	// in 32 bits —, or an IP type.
	ipHeldBy = holdsText | holdsNumber | holdsIP
)

var (
	// personalNames are fragments of a field's name that read like personal
	// data, with what can hold the value they mention; namedIP are whole names
	// of the address a machine was reached from, which ipHeldBy holds.
	personalNames = []struct {
		fragments []string
		heldBy    holding
	}{
		// An address to write to, a person's name, a bank account: text.
		{[]string{"email", "street", "firstname", "lastname", "givenname", "familyname", "surname", "fullname", "iban"}, holdsText},
		// What is written with digits: text, or a number.
		{[]string{"phone", "mobile", "postcode", "postalcode", "zipcode", "passport", "nationalid", "taxid"}, holdsText | holdsNumber},
		// A birth: text, a number — a year, a timestamp —, or an instant.
		{[]string{"birth"}, holdsText | holdsNumber | holdsTime},
		// A postal address or a network one: text, or an IP type. Not a
		// number: a fragment this common names an address's ID or count.
		{[]string{"address"}, holdsText | holdsIP},
	}
	// namedIP are the names of a field that holds an IP address.
	namedIP = []string{"ip", "ipaddr", "clientip", "remoteip", "userip", "remoteaddr"}

	// knownHolding are the types that hold what their fields do not say: an
	// instant, and an IP address.
	knownHolding = map[reflect.Type]holding{
		timeTyp:                           holdsTime,
		reflect.TypeFor[net.IP]():         holdsIP,
		reflect.TypeFor[netip.Addr]():     holdsIP,
		reflect.TypeFor[netip.AddrPort](): holdsIP,
		reflect.TypeFor[netip.Prefix]():   holdsIP,
	}

	// credentialNames are fragments of a name that say a credential, which is
	// what secret holds: a hash, a token, a key. A secret field whose name says
	// one is not warned of, whatever else it says — an e-mail's hash, a
	// phone's token.
	credentialNames = []string{"password", "passwd", "passphrase", "token", "key", "hash", "digest", "secret", "salt", "credential"}
)

// The name heuristic (ADR 0006 §1): kit warns of a field that has no class
// and reads like personal data — its name mentions some, and its type can
// hold what the name mentions —, once per field where it is declared. It
// warns too of a field tagged secret that reads like personal data and not
// like a credential: secret was once the only word that hid a value, and
// means a credential since ADR 0006 — never exported, listed as one.

// holding is what a value of a Go type can hold, as the name heuristic asks
// it.
type holding uint8

// Once per declared field -----------------------------------------------------

// declaredField is a struct field where it is declared: the struct that
// declares it, and its index there. A field promoted into several structs
// is one declared field.
type declaredField struct {
	owner reflect.Type
	index int
}

// fieldWarning is a warning about one declared field.
type fieldWarning struct {
	field declaredField
	said  phrase
}

// looksPersonal reports whether a field of type t reads like personal data:
// one of its names — JSON or Go — mentions some, and t can hold what the
// name mentions (holdingOf). An e-mail, a street, a person's name and an
// IBAN are text; a phone number, a postcode, a passport's, a national
// identity's or a tax number are text or a number; a birth is text, a
// number or an instant; an address is text or an IP type; an IP, which only
// a whole name says, is text, a number or an IP type. So a bool is never
// warned of; a time.Time only when its name mentions a birth; a number when
// its name mentions a birth, an IP or what is written with digits; net.IP
// and netip.Addr when their name mentions an address or an IP; a string,
// bytes and an interface whatever personal data their name mentions.
func looksPersonal(t reflect.Type, names ...string) bool {
	held := holdingOf(t)
	for _, name := range names {
		n := normalName(name)
		if held&ipHeldBy != 0 && slices.Contains(namedIP, n) {
			return true
		}
		for _, p := range personalNames {
			if held&p.heldBy != 0 && slices.ContainsFunc(p.fragments, func(f string) bool { return strings.Contains(n, f) }) {
				return true
			}
		}
	}
	return false
}

// secretLooksPersonal reports whether a field tagged secret reads like
// personal data rather than a credential: none of its names says a
// credential, and one of them mentions personal data its type can hold
// (looksPersonal).
func secretLooksPersonal(t reflect.Type, names ...string) bool {
	for _, name := range names {
		n := normalName(name)
		if slices.ContainsFunc(credentialNames, func(f string) bool { return strings.Contains(n, f) }) {
			return false
		}
	}
	return looksPersonal(t, names...)
}

// normalName lower-cases a name and keeps its letters and digits:
// "first_name", "firstName" and "First-Name" read alike.
func normalName(name string) string {
	b := make([]byte, 0, len(name))
	for i := range len(name) {
		c := name[i]
		switch {
		case c >= 'A' && c <= 'Z':
			b = append(b, c+'a'-'A')
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			b = append(b, c)
		}
	}
	return string(b)
}

// kindHolding is what a value of kind k holds when its type says no more:
// a string is text, an interface anything, a number a number; a bool, a
// function or a channel nothing.
func kindHolding(k reflect.Kind) holding {
	switch k {
	case reflect.String:
		return holdsText
	case reflect.Interface:
		return holdsAnything
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64, reflect.Complex64, reflect.Complex128:
		return holdsNumber
	default:
		// Every other kind holds nothing a heuristic looks at.
	}
	return 0
}

// holdingOf is what a value of t can hold, read from its Go type rather
// than from the JSON it writes: a type that writes its own JSON writes it
// from its fields. A pointer holds what it points to; a slice or an array
// what its elements hold, bytes being text; a map what its keys and its
// values hold; a struct what its fields hold, exported or not — time.Time
// and the IP types excepted (knownHolding) —; any other type what its kind
// holds (kindHolding).
func holdingOf(t reflect.Type) holding { return holdingIn(t, map[reflect.Type]bool{}) }

// holdingIn is holdingOf, seen holding the structs already read: a struct
// met again holds nothing more.
func holdingIn(t reflect.Type, seen map[reflect.Type]bool) holding {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if h, ok := knownHolding[t]; ok {
		return h
	}
	switch t.Kind() {
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 {
			return holdsText
		}
		return holdingIn(t.Elem(), seen)
	case reflect.Map:
		return holdingIn(t.Key(), seen) | holdingIn(t.Elem(), seen)
	case reflect.Struct:
		return structHolding(t, seen)
	default:
		return kindHolding(t.Kind())
	}
}

// structHolding is what a struct's fields can hold, each struct walked once.
func structHolding(t reflect.Type, seen map[reflect.Type]bool) holding {
	if seen[t] {
		return 0
	}
	seen[t] = true
	var h holding
	for f := range t.Fields() {
		h |= holdingIn(f.Type, seen)
	}
	return h
}

// declaredAt follows index — a field's path from the struct t through the
// structs it embeds, as FieldByIndex takes it — to where the field is
// declared.
func declaredAt(t reflect.Type, index []int) declaredField {
	d := declaredField{owner: t}
	for n, i := range index {
		if n > 0 {
			d.owner = d.owner.Field(d.index).Type
			for d.owner.Kind() == reflect.Pointer {
				d.owner = d.owner.Elem()
			}
		}
		d.index = i
	}
	return d
}

// looksPersonalWarning warns of the field at index in the struct t, named
// goName, whose name and type read like personal data: it names the struct
// that declares the field, not the one the field is promoted into.
func looksPersonalWarning(t reflect.Type, index []int, goName string) fieldWarning {
	at := declaredAt(t, index)
	return fieldWarning{field: at, said: say("classify.looks-personal", "field", fieldLabel(at.owner, goName))}
}

// secretPersonalWarning warns of the field at index in the struct t, tagged
// secret, whose name reads like personal data: kit never exports a secret,
// and the register lists it as a credential (ADR 0006, the migration from
// secret to personal).
func secretPersonalWarning(t reflect.Type, index []int, goName string) fieldWarning {
	at := declaredAt(t, index)
	return fieldWarning{field: at, said: say("classify.secret-personal", "field", fieldLabel(at.owner, goName))}
}

// uniqueWarnings keeps the first warning of each declared field, in order.
func uniqueWarnings(in []fieldWarning) []fieldWarning {
	seen := map[declaredField]bool{}
	out := in[:0]
	for _, w := range in {
		if !seen[w.field] {
			seen[w.field] = true
			out = append(out, w)
		}
	}
	return out
}
