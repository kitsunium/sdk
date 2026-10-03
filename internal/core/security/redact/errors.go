// Package redact — declares the sentinel *errs.Error outcomes. Each var's name
// equals its errs.Define Reason in SCREAMING_SNAKE form.
//
// Neither refusal carries a byte of what it refused: this domain exists to
// keep secrets out of what is shown, and an error message is shown. They are
// raised by the engine in internal/service/security/redact — their Private
// names that package, the one that raises them — and declared here since ADR
// 0160, so every code of the domain is in one place.
package redact

import "github.com/kitsunium/sdk/internal/kernel/errs"

var (
	// DocumentInvalid refuses a document that is not one well-formed JSON
	// value. Nothing is returned for it: a partial copy of a document that
	// does not parse cannot be trusted to have had its secrets recognised.
	DocumentInvalid = errs.Define(CodeDocumentInvalid, "DOCUMENT_INVALID",
		"The document is not valid JSON and was not redacted",
		"service/security/redact: the JSON reader refused the document; the offset field locates where")

	// ValueUnencodable refuses a value encoding/json will not encode, so
	// there is no JSON form to redact.
	ValueUnencodable = errs.Define(CodeValueUnencodable, "VALUE_UNENCODABLE",
		"The value cannot be encoded as JSON and was not redacted",
		"service/security/redact: encoding/json refused the value; the type field names its Go type")
)
