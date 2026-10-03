// Package strictjson — decoding the JSON body of an HTTP request.
package strictjson

import (
	"bufio"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Media types a request body may declare to be read as JSON.
const (
	// jsonMediaType is RFC 8259's registration.
	jsonMediaType string = "application/json"
	// jsonSuffix is the structured-syntax suffix of RFC 6839, §3.1: any
	// media type ending in it — application/problem+json,
	// application/vnd.acme+json — is JSON with a more specific meaning.
	jsonSuffix string = "+json"
)

// DecodeRequest decodes the JSON body of req into v, which must be a non-nil
// pointer, exactly as Decode does, reading at most maxBytes bytes.
//
// Around Decode it adds the three things only an HTTP body has:
//
//   - the body is read through http.MaxBytesReader, so a body past the bound
//     is DocumentTooLarge (413) AND net/http is told to close the connection
//     rather than drain what the client keeps sending;
//   - an empty body is DocumentEmpty before anything else is looked at,
//     whatever the Content-Type says — a caller for whom the body is optional
//     treats that code as "no body", one for whom it is required answers 400;
//   - a non-empty body must declare application/json or a +json type (RFC
//     6839), or it is MediaTypeUnsupported (415) and is not parsed at all.
//
// w is used only to tell net/http to close the connection; nil is accepted.
func DecodeRequest(w http.ResponseWriter, req *http.Request, v any, maxBytes int64) error {
	//: a call that can never succeed is refused before reading a byte.
	if invalid := checkArguments(v, maxBytes); invalid != nil {
		//: DecodeMisconfigured, naming the argument.
		return invalid
	}
	//: a request without a body to read is the caller's bug, not the client's.
	if req == nil || req.Body == nil {
		//: DecodeMisconfigured, naming the argument.
		return misconfigured("req")
	}
	body := bufio.NewReader(http.MaxBytesReader(w, req.Body, maxBytes))
	//: whether there is a body at all is settled first: an empty body has no
	//: media type worth refusing.
	if _, peekErr := body.Peek(1); peekErr != nil {
		//: DocumentEmpty, or the transport's failure.
		return emptyOrUnreadable(peekErr)
	}
	//: a body that does not say it is JSON is not parsed as JSON.
	if !isJSONMediaType(req.Header.Get("Content-Type")) {
		//: 415, before a byte is decoded.
		return MediaTypeUnsupported
	}
	//: the bound is net/http's too, so its refusal reads as the size it is.
	return decode(body, v, maxBytes, isMaxBytesError)
}

// emptyOrUnreadable classifies a failed first read.
func emptyOrUnreadable(peekErr error) error {
	//: no first byte: an empty body.
	if errors.Is(peekErr, io.EOF) {
		//: DocumentEmpty.
		return DocumentEmpty
	}
	//: the transport failed before the body began: its type, never its text.
	return errs.Wrap(DocumentUnreadable, errs.WrapParams{}, errs.String(fieldCause, causeOf(peekErr)))
}

// isJSONMediaType reports whether a Content-Type header declares JSON.
func isJSONMediaType(header string) bool {
	media, _, parseErr := mime.ParseMediaType(header)
	//: absent or unparsable is not a declaration.
	if parseErr != nil {
		//: not JSON.
		return false
	}
	//: ParseMediaType lower-cases the type, so the comparison is exact.
	return media == jsonMediaType || strings.HasSuffix(media, jsonSuffix)
}

// isMaxBytesError recognises net/http's refusal of a body past its bound.
func isMaxBytesError(err error) bool {
	tooLarge, found := errors.AsType[*http.MaxBytesError](err)
	//: the one read error that is a size, not a transport failure.
	return found && tooLarge != nil
}
