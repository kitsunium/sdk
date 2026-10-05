package crypto

import (
	"strconv"
	"strings"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// exitDataErr matches sysexits EX_DATAERR — a bad key or undecryptable box is a
// data problem, not a generic internal software error (70).
const exitDataErr int = 65

// httpBadRequest is the HTTP status a decryption failure maps to: the supplied
// ciphertext is client-bad input, not a server fault.
const httpBadRequest int = 400

// conflictText renders a registry conflict for the boot panic that reports it:
// the typed error's header and Public, then each field it carries, quoted.
// Error() never renders a field (rule 4), and the panic is the only place an
// operator learns WHICH registrar refused WHICH key.
func conflictText(err error) string {
	var b strings.Builder
	//: the canonical "[0.2.4.1 DUPLICATE_REGISTRATION] <public>" header.
	b.WriteString(err.Error())
	//: then every field, in the order the conflict attached them.
	for i, f := range errs.FieldsOf(err) {
		//: a colon before the first field, a space between the others.
		if i == 0 {
			b.WriteString(":")
		}
		b.WriteString(" " + f.Key() + "=" + strconv.Quote(f.StringValue()))
	}
	//: one line, ready to panic with.
	return b.String()
}
