// Package jsonpatch — the sentinel *errs.Error values, one per code. Each
// Reason derives from its var name or from its Code constant (ADR 0020);
// the Public and Private texts are the ones the service package always
// emitted, so moving the declaration changed no rendering.
//
// No Public and no Private below carries a byte of either document: a
// document compared is routinely a record holding personal data, and an error
// is where input classically leaks back out.
package jsonpatch

import "github.com/kitsunium/sdk/internal/kernel/errs"

// exitDataErr matches sysexits EX_DATAERR (65): the input was wrong.
const exitDataErr int = 65

// httpBadRequest is RFC 9110 400 Bad Request: a document that is not JSON is
// the caller's input, not a server fault. A literal, because the core does
// not import net/http for a number (ADR 0160).
const httpBadRequest int = 400

// NotJSON refuses a document that is not exactly one JSON value. The fields
// name which document — "from" or "to" — and the byte offset where reading
// stopped, never what the document held.
var NotJSON = errs.Define(CodeNotJSON, "NOT_JSON",
	"A document to compare is not a JSON value",
	"service/data/codec/jsonpatch: a document is not exactly one well-formed JSON value; the fields name the document and the offset, never its content",
	errs.WithHTTPStatus(httpBadRequest), errs.WithExitCode(exitDataErr))
