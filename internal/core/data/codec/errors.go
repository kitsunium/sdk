// Package codec — declares the registry's boot-time sentinel. Its var name
// equals its errs.Define Reason in SCREAMING_SNAKE form.
package codec

import (
	"strconv"
	"strings"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// DuplicateRegistration is the conflict Register refuses at boot: a distinct
// codec already holds the Format name, MIME type or extension a second one
// claims. It is never returned — Register panics with conflictText of it, so
// the panic carries the dotted-quad header AND the key that collided.
var DuplicateRegistration = errs.Define(CodeDuplicateRegistration, "DUPLICATE_REGISTRATION",
	"A codec is already registered under that name, MIME type or extension",
	"core/data/codec.Register: a distinct codec already holds this Format name, MIME type or extension")

// conflictText renders a registry conflict for the boot panic that reports it:
// the typed error's header and Public, then each field it carries, quoted.
// Error() never renders a field (rule 4), and the panic is the only place an
// operator learns WHICH key collided and which codec already held it.
func conflictText(err error) string {
	var b strings.Builder
	//: the canonical "[0.2.2.1 DUPLICATE_REGISTRATION] <public>" header.
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
