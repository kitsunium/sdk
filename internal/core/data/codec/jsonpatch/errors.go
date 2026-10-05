package jsonpatch

// exitDataErr matches sysexits EX_DATAERR (65): the input was wrong.
const exitDataErr int = 65

// httpBadRequest is RFC 9110 400 Bad Request: a document that is not JSON is
// the caller's input, not a server fault. A literal, because the core does
// not import net/http for a number (ADR 0160).
const httpBadRequest int = 400
