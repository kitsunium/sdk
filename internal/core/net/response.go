// Package net — the completed outbound response value.
package net

import "net/http"

// ResponseValue is a fully-read outbound response.
//
// The client returns this rather than an *http.Response on purpose. A returned
// *http.Response makes the caller responsible for closing the body and for
// reading it under a size cap, and a single forgotten Close leaks a connection.
// It also makes an accurate byte count impossible: the size of a body is only
// known once it has been read, so an observation hook firing at header time can
// never report it. Reading and closing inside the client fixes both.
type ResponseValue struct {
	// Status is the HTTP status code. It is set even when the call also returns
	// an error, so a caller can tolerate a specific status deliberately.
	Status int
	// Header is the response header set.
	Header http.Header
	// Body is the fully-read response body, already bounded by the client's
	// size cap. It is nil for a status that carries no body, such as 204.
	Body []byte
}
