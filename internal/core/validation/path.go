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
// The member name is the GO FIELD NAME, not a serialization tag. The SDK
// cannot know which of a field's tags — json, xml, yaml, form — names it on
// the surface a given caller is answering, and picking one would be wrong on
// the others; the Go name is the single identifier that is always right and
// always greppable. A caller answering a JSON API rewrites the paths at that
// edge, where the mapping is known.
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
