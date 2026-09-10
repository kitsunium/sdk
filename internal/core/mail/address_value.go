// Package mail — the mailbox value.
package mail

// AddressValue is one RFC 5322 mailbox: an addr-spec and an optional display
// name.
//
// It is deliberately NOT net/mail.Address, and the reason is measurable rather
// than aesthetic. net/mail.Address.String() emits its Address field verbatim:
//
//	mail.Address{Name: "ok", Address: "u@exa\r\nmple.com"}.String()
//	  == "\"ok\" <u@exa\r\nmple.com>"
//
// The CRLF survives into the header, which is the injection this whole domain
// exists to refuse. The display name is protected there (it is RFC 2047
// encoded, so a CRLF becomes "=0D=0A") and the address is not — so a caller who
// reached for the obvious stdlib helper would be protected on the field an
// attacker rarely controls and exposed on the one they usually do.
type AddressValue struct {
	// Name is the optional display name. It may hold any printable text,
	// including non-ASCII, which is RFC 2047 encoded at composition. CR, LF and
	// NUL are refused.
	Name string
	// Addr is the addr-spec: "local@domain", ASCII, in the dot-atom form of
	// RFC 5322 §3.4.1. Angle brackets are added by the composer and must not
	// appear here.
	Addr string
}

// IsZero reports whether the address carries no addr-spec at all, which is the
// zero value a caller gets from an unfilled struct field.
func (a AddressValue) IsZero() bool {
	//: a display name with no mailbox is not an address, it is a label.
	return a.Addr == ""
}
