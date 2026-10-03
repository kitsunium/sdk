// Package mail — one additional header, as a value.
package mail

// HeaderFieldValue is one additional header: a name and a value.
//
// It is a slice element rather than a map entry for two reasons that both
// matter: a map iterates in a random order, so the same message would compose
// to different bytes on different runs and no golden test could exist; and
// some fields legitimately repeat (Received, References), which a map cannot
// express at all.
type HeaderFieldValue struct {
	// Name is the field name — printable US-ASCII without a colon
	// (RFC 5322 §3.6.8).
	Name string
	// Value is the unstructured field value. It is encoded per RFC 2047 when it
	// carries non-ASCII, folded to the RFC 5322 §2.1.1 line limits, and refused
	// outright when it carries CR, LF or NUL.
	Value string
}
