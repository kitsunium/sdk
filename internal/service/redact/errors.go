// Package redact — declares the sentinel *errs.Error outcomes. Each var's name
// equals its errs.Define Reason in SCREAMING_SNAKE form.
//
// Neither refusal carries a byte of what it refused: this package exists to
// keep secrets out of what is shown, and an error message is shown.
package redact

import "github.com/kitsunium/sdk/internal/kernel/errs"

var (
	// DocumentInvalid refuses a document that is not one well-formed JSON
	// value. Nothing is returned for it: a partial copy of a document that
	// does not parse cannot be trusted to have had its secrets recognised.
	DocumentInvalid = errs.Define(CodeDocumentInvalid, "DOCUMENT_INVALID",
		"The document is not valid JSON and was not redacted",
		"service/redact: the JSON reader refused the document; the offset field locates where")

	// ValueUnencodable refuses a value encoding/json will not encode, so
	// there is no JSON form to redact.
	ValueUnencodable = errs.Define(CodeValueUnencodable, "VALUE_UNENCODABLE",
		"The value cannot be encoded as JSON and was not redacted",
		"service/redact: encoding/json refused the value; the type field names its Go type")
)
