package jwk

// exitDataErr matches sysexits EX_DATAERR — a malformed JWK is a data problem,
// not a generic internal software error (70).
const exitDataErr int = 65

// httpBadRequest is the HTTP status a malformed JWK maps to: the document is
// client-bad input, not a server fault.
const httpBadRequest int = 400

// httpNotFound is the HTTP status an unresolvable "kid" maps to: the caller
// named a key the set does not hold.
const httpNotFound int = 404
