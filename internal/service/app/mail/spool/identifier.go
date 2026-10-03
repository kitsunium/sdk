// Package spool — a mail's identifier: the one rule it keeps, whoever minted
// it, because it becomes the left half of the mail's Message-ID.
package spool

import coremail "github.com/kitsunium/sdk/internal/core/app/mail"

// MaxIDBytes bounds a mail's identifier, whoever minted it. It is well above
// every identifier the SDK mints — a UUID, a ULID, a TypeID under the longest
// prefix TypeID allows — and low enough that "Message-ID: <id@domain>" keeps
// within RFC 5322's line limit beside the longest domain an address may carry.
const MaxIDBytes int = 255

// checkID reports whether id is an identifier a mail can keep, and names the
// rule it breaks when it is not. An identifier is non-empty, at most
// MaxIDBytes, and an RFC 5322 dot-atom — the grammar of a Message-ID's left
// half — so it is printable ASCII: it can neither end a header nor split one,
// and it comes back from the spool's JSON as the bytes it went in as.
func checkID(id string) (problem string, ok bool) {
	//: the three rules, the cheapest first.
	switch {
	//: an empty identifier is one every later empty one would repeat.
	case id == "":
		//: named.
		return "empty", false
	//: past the bound.
	case len(id) > MaxIDBytes:
		//: named, by the bound it broke.
		return "longer than MaxIDBytes", false
	//: a byte a Message-ID cannot carry, or an empty label.
	case !coremail.IsDotAtom(id):
		//: named, by the grammar it broke.
		return "not an RFC 5322 dot-atom", false
	//: an identifier a mail can keep.
	default:
		//: nothing to name.
		return "", true
	}
}
