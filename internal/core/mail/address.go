// Package mail — the addr-spec subset this domain accepts, and the display-name
// rule the composer depends on.
package mail

import (
	"strings"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// RFC 5321 §4.5.3.1 octet ceilings. They are the protocol's, not this domain's:
// a longer local part or domain is not merely unusual, it is undeliverable, and
// discovering that after the DATA command has been accepted is worse than
// discovering it in a constructor.
const (
	maxLocalPartOctets int = 64
	maxDomainOctets    int = 255
)

// specials is RFC 5322 §3.2.3's "specials" production. A display name
// containing one of these MUST be a quoted-string, or the receiver parses the
// name as structure: "Doe, John <j@x>" is two addresses, one of them
// syntactically broken, and every client shows something different.
const specials string = `()<>[]:;@\,."`

// ValidateAddress reports whether a is a mailbox this domain will carry, and
// returns a typed refusal naming field — "From", "To[1]", "Cc[0]" — when it is
// not.
//
// The accepted grammar is a deliberate SUBSET of RFC 5322 §3.4.1: an ASCII
// dot-atom local part, one "@", and an ASCII dot-atom domain, inside the
// RFC 5321 §4.5.3.1 length limits. Two legal forms are refused BY NAME rather
// than mangled:
//
//   - A quoted local part (`"john doe"@example.com`) is legal RFC 5322 and is
//     refused, because quoting and unquoting it correctly through a display
//     name, an envelope and a log line is four chances to get it wrong for a
//     form essentially nobody uses.
//   - A non-ASCII addr-spec (RFC 6531 SMTPUTF8) is refused because SMTPUTF8
//     must be negotiated with the server and net/smtp does not negotiate it.
//     The alternatives are worse than a refusal: punycoding the domain or
//     stripping accents from the local part both produce an address that
//     delivers, to somebody else.
func ValidateAddress(field string, a AddressValue) error {
	//: the display name reaches a header, so it faces the injection gate first.
	if nameErr := ValidateHeaderValue(field, a.Name); nameErr != nil {
		//: HeaderInjection.
		return nameErr
	}
	//: an empty addr-spec is the unfilled-struct case; every position but From
	//: reports it as invalid, and From has its own verdict in Validate.
	if a.Addr == "" {
		//: the field position is diagnostic, the (empty) value is not secret.
		return errs.Wrap(InvalidAddress, errs.WrapParams{}, errs.String("field", field))
	}
	//: the addr-spec faces the injection gate too — an unvalidated "@" split
	//: would happily accept a CRLF on either side of it.
	if addrErr := ValidateHeaderValue(field, a.Addr); addrErr != nil {
		//: HeaderInjection.
		return addrErr
	}
	//: the two named-and-refused forms, before the grammar, so the caller hears
	//: "not supported" rather than "invalid" for something that is valid mail.
	if unsupportedErr := refuseUnsupportedAddress(field, a.Addr); unsupportedErr != nil {
		//: UnsupportedAddress.
		return unsupportedErr
	}
	//: and finally the dot-atom grammar and the length ceilings.
	return validateAddrSpec(field, a.Addr)
}

// refuseUnsupportedAddress names the two legal address forms this domain
// declines to carry, so that neither is reported as a syntax error.
func refuseUnsupportedAddress(field, addr string) error {
	//: any octet above the printable ASCII range means the address is not
	//: ASCII, which is RFC 6531 SMTPUTF8 territory and needs an extension
	//: nothing here negotiates.
	for index := range len(addr) {
		//: byte-wise: a multi-byte rune has every continuation byte up there.
		if addr[index] > maxPrintableASCII {
			//: named, not transliterated.
			return errs.Wrap(UnsupportedAddress, errs.WrapParams{},
				errs.String("field", field), errs.String("form", "smtputf8"))
		}
	}
	//: a leading quote is the quoted-string local-part form of RFC 5322 §3.4.1.
	if strings.HasPrefix(addr, `"`) {
		//: named, not unquoted.
		return errs.Wrap(UnsupportedAddress, errs.WrapParams{},
			errs.String("field", field), errs.String("form", "quoted-local-part"))
	}
	//: a carriable form.
	return nil
}

// validateAddrSpec applies the dot-atom grammar and the RFC 5321 §4.5.3.1
// ceilings to an addr-spec already known to be ASCII and free of CR, LF and
// NUL.
func validateAddrSpec(field, addr string) error {
	//: exactly one "@": zero is not an address, two is ambiguous, and the
	//: quoted form that would legitimise a second one is already refused.
	at := strings.IndexByte(addr, '@')
	//: a second "@" anywhere after the first is fatal.
	if at <= 0 || at == len(addr)-1 || strings.IndexByte(addr[at+1:], '@') >= 0 {
		//: the field position travels, the address does not.
		return errs.Wrap(InvalidAddress, errs.WrapParams{}, errs.String("field", field))
	}
	local, domain := addr[:at], addr[at+1:]
	//: the protocol's ceilings, checked before anything is sent rather than
	//: after a server rejects the RCPT.
	if len(local) > maxLocalPartOctets || len(domain) > maxDomainOctets {
		//: the length is diagnostic and reveals nothing about the address.
		return errs.Wrap(InvalidAddress, errs.WrapParams{},
			errs.String("field", field), errs.Int("local_octets", len(local)),
			errs.Int("domain_octets", len(domain)))
	}
	//: both halves must be dot-atoms: printable ASCII, no specials, no space,
	//: and no empty label at either end or in the middle.
	if !isDotAtom(local) || !isDotAtom(domain) {
		//: invalid, and the caller's own literal is the only thing they need.
		return errs.Wrap(InvalidAddress, errs.WrapParams{}, errs.String("field", field))
	}
	//: a deliverable, unambiguous mailbox.
	return nil
}

// isDotAtom reports whether s is an RFC 5322 §3.2.3 dot-atom: one or more
// atoms of atext separated by single dots, with no leading, trailing or
// doubled dot.
func isDotAtom(s string) bool {
	//: an empty half cannot be an atom; a leading, trailing or doubled dot is
	//: an empty label, which no MTA accepts. One guard, one verdict.
	if s == "" || strings.HasPrefix(s, ".") || strings.HasSuffix(s, ".") || strings.Contains(s, "..") {
		//: rejected.
		return false
	}
	//: and every remaining octet must be atext.
	return isAtextRun(s)
}

// isAtextRun reports whether every octet of s is atext or the dot separator:
// printable ASCII that is neither a special nor a space.
func isAtextRun(s string) bool {
	//: one pass, no allocation.
	for index := range len(s) {
		char := s[index]
		//: the dot is the separator and is legal between atoms.
		if char == '.' {
			//: keep scanning.
			continue
		}
		//: printable ASCII only, specials excluded.
		if char <= spaceOctet || char >= delOctet || strings.IndexByte(specials, char) >= 0 {
			//: rejected.
			return false
		}
	}
	//: every octet is atext.
	return true
}

// NeedsQuotedDisplayName reports whether name must be emitted as an RFC 5322
// §3.2.4 quoted-string rather than as a bare sequence of atoms.
//
// It is exported because the composer is in another package and this is a
// property of the GRAMMAR, not of the writer. Getting it wrong is the classic
// address bug: an unquoted "Doe, John" makes the comma a list separator, so a
// message to one person is parsed as a message to two, one of which is not an
// address at all.
func NeedsQuotedDisplayName(name string) bool {
	//: an empty name is omitted entirely rather than quoted.
	if name == "" {
		//: nothing to quote.
		return false
	}
	//: surrounding whitespace is lost by an unquoted atom sequence.
	if name != strings.TrimSpace(name) {
		//: quote it.
		return true
	}
	//: any special turns the name into structure the receiver will parse.
	return strings.ContainsAny(name, specials)
}
