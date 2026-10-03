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

var (
	// WriterUnknownName is returned by Open when no factory is registered
	// under the requested Name — typically a missing blank-import.
	WriterUnknownName = errs.Define(CodeWriterUnknownName, "WRITER_UNKNOWN_NAME",
		"No writer is registered under that name",
		"core/writer.Open: name absent from registry; blank-import the writer's package to register it")

	// WriterConfigInvalid is the shared sentinel every factory returns when
	// the supplied Config is of the wrong concrete type. Factories attach an
	// errs Field naming the writer so the offender is identifiable.
	WriterConfigInvalid = errs.Define(CodeWriterConfigInvalid, "WRITER_CONFIG_INVALID",
		"Writer configuration has the wrong type",
		"core/writer factory received a Config of an unexpected concrete type")

	// DuplicateRegistration is the conflict Register refuses at boot: a
	// distinct factory already holds the Name a second one claims. Register
	// panics with conflictText of it, so the panic carries the dotted-quad
	// header AND the Name that collided.
	DuplicateRegistration = errs.Define(CodeDuplicateRegistration, "DUPLICATE_REGISTRATION",
		"A writer is already registered under that name",
		"core/writer.Register: a distinct factory already holds this Name")
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
