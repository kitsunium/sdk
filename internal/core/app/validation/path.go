// Package validation — defines the path grammar every ViolationValue speaks.
package validation

import "strconv"

// RootPath is the path of the value handed to a validator, before any descent.
// A violation reported at RootPath is about the value as a whole — a
// cross-field rule with no single culpable member.
const RootPath string = ""

// fieldSeparator joins a parent path to a member name.
const fieldSeparator string = "."

// indexOpen and indexClose bracket an element position.
const (
	indexOpen  string = "["
	indexClose string = "]"
)

// JoinField extends base with a struct member, producing the dotted form the
// grammar defines:
//
//	JoinField(RootPath, "user")        == "user"
//	JoinField("user", "address")       == "user.address"
//	JoinField("user.addresses[2]", "zip") == "user.addresses[2].zip"
//
// name is whatever the calling front end names the member. The struct-tag
// front end passes the json tag's name when the field has one, else the Go
// field name (ADR 0046): the SDK's own config.Load decodes every format through
// a JSON round trip, so the json name is the key the operator actually wrote.
// The programmatic Field takes the name as an argument, so there the caller
// decides.
//
// The grammar does not quote. A member name that itself contains '.', '[' or
// ']' — json:"log.level" is legal — yields a path indistinguishable from a
// nested or indexed one. The quoted segment that would disambiguate it is the
// same one map descent needs, and ADR 0046 §Deferred defers both together
// rather than growing the grammar for one of them.
func JoinField(base, name string) string {
	//: at the root there is no parent to separate from.
	if base == RootPath {
		//: the member name IS the path.
		return name
	}
	//: an empty member name would produce a trailing dot that reads as a
	//: truncated path; treat it as "no descent happened".
	if name == "" {
		//: keep the parent path intact.
		return base
	}
	//: dotted descent.
	return base + fieldSeparator + name
}

// JoinIndex extends base with an element position, producing the bracketed
// form the grammar defines:
//
//	JoinIndex("addresses", 2) == "addresses[2]"
//
// The bracket binds tighter than the dot: "a.b[0].c" is element 0 of a.b, and
// there is never a dot before an opening bracket.
func JoinIndex(base string, index int) string {
	//: strconv.Itoa rather than fmt so the grammar has no formatting verb in
	//: it and the hot path stays allocation-cheap.
	return base + indexOpen + strconv.Itoa(index) + indexClose
}
