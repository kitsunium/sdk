//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/data/codec/strictjson/httpbody .

// Package httpbody decodes the JSON body of an HTTP request with the strict
// decoder of github.com/kitsunium/sdk/pkg/v1/data/codec/strictjson, adding
// what only an HTTP body has.
//
//	var in CreateItem
//	if err := httpbody.DecodeRequest(w, r, &in, 1<<20); err != nil {
//		http.Error(w, errs.PublicOf(err), errs.HTTPStatusOf(err)) // 400, 413 or 415
//		return
//	}
//
// # What a request body adds
//
// The body is read through http.MaxBytesReader, so a body past the bound also
// tells net/http to close the connection instead of draining what the client
// keeps sending. An empty body is strictjson's CodeDocumentEmpty before its
// Content-Type is looked at — treat that code as "no body" where the body is
// optional. A non-empty body must declare application/json or a
// structured-syntax +json type (RFC 6839), or it is strictjson's
// CodeMediaTypeUnsupported (415) and is not parsed at all.
//
// Everything else is strictjson's: the bound, the refusals and their codes —
// match them with errs.HasCode and the strictjson constants — and PointerOf,
// which locates a refused body as it locates any refused document.
//
// # Why a package of its own
//
// So that strictjson links no net/http. A program decoding documents from
// files, queues or sockets imports strictjson alone; a server imports this
// package too, and the net/http it serves with anyway.
package httpbody

import (
	"net/http"

	svchttpbody "github.com/kitsunium/sdk/internal/service/data/codec/strictjson/httpbody"
)

// DecodeRequest decodes the JSON body of req into v, a non-nil pointer, as
// strictjson's Decode does, through http.MaxBytesReader, after refusing an
// empty body (CodeDocumentEmpty) and a body that does not declare JSON
// (CodeMediaTypeUnsupported). w is used only to have net/http close the
// connection after an oversized body; nil is accepted.
func DecodeRequest(w http.ResponseWriter, req *http.Request, v any, maxBytes int64) error {
	//: delegate verbatim to the service implementation.
	return svchttpbody.DecodeRequest(w, req, v, maxBytes)
}
