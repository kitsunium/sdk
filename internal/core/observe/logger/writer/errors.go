// Package writer — declares the sentinels returned by the registry and shared
// across factories. Each var's name equals its errs.Define Reason in
// SCREAMING_SNAKE form. DuplicateRegistration is never returned: Register
// panics at boot with conflictText of it. CodeWriterNil and CodeWriterNameEmpty
// label boot panics too, spelled in the message (see registry.go).
package writer

import (
	"strconv"
	"strings"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// conflictText renders a registry conflict for the boot panic that reports it:
// the typed error's header and Public, then each field it carries, quoted.
// Error() never renders a field (rule 4), and the panic is the only place an
// operator learns WHICH Name collided.
func conflictText(err error) string {
	var b strings.Builder
	//: the canonical "[0.2.3.1 DUPLICATE_REGISTRATION] <public>" header.
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
