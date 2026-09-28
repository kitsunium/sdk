// Package jsonpatch — declares the sentinel *errs.Error outcomes. Each var's
// name equals its errs.Define Reason in SCREAMING_SNAKE form.
//
// No Public and no Private below carries a byte of either document: a
// document compared is routinely a record holding personal data, and an error
// is where input classically leaks back out.
package jsonpatch

import (
	"net/http"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// exitDataErr matches sysexits EX_DATAERR (65): the input was wrong.
const exitDataErr int = 65

// NotJSON refuses a document that is not exactly one JSON value. The fields
// name which document — "from" or "to" — and the byte offset where reading
// stopped, never what the document held.
var NotJSON = errs.Define(CodeNotJSON, "NOT_JSON",
	"A document to compare is not a JSON value",
	"service/codec/jsonpatch: a document is not exactly one well-formed JSON value; the fields name the document and the offset, never its content",
	errs.WithHTTPStatus(http.StatusBadRequest), errs.WithExitCode(exitDataErr))
