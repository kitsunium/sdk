package http1

import (
	"bytes"
	"strconv"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// The status codes the writer reasons about.
const (
	// statusOK is what a zero HTTP1ResponseValue.Status means.
	statusOK int = 200
	// statusNoContent carries neither a length nor a body.
	statusNoContent int = 204
	// statusNotModified carries neither a length nor a body.
	statusNotModified int = 304
	// statusSwitching is the one interim status the profile sends, for Upgrade.
	statusSwitching int = 101
	// statusInternal answers what the writer refused or a handler left deferred.
	statusInternal int = 500
	// statusMin is the lowest status the profile sends: no interim response.
	statusMin int = 200
	// statusMax is the highest three-digit status.
	statusMax int = 999
	// statusRegistered is one past the highest code statusLines spells.
	statusRegistered int = 600
	// decimal is the base a status and a Content-Length are written in.
	decimal int = 10
)

var (
	// statusLines are the status lines of the codes RFC 9110 §15 registers,
	// spelled once, indexed by code. An entry left empty is composed on demand.
	statusLines = [statusRegistered]string{
		200: "HTTP/1.1 200 OK\r\n",
		201: "HTTP/1.1 201 Created\r\n",
		202: "HTTP/1.1 202 Accepted\r\n",
		203: "HTTP/1.1 203 Non-Authoritative Information\r\n",
		204: "HTTP/1.1 204 No Content\r\n",
		205: "HTTP/1.1 205 Reset Content\r\n",
		206: "HTTP/1.1 206 Partial Content\r\n",
		300: "HTTP/1.1 300 Multiple Choices\r\n",
		301: "HTTP/1.1 301 Moved Permanently\r\n",
		302: "HTTP/1.1 302 Found\r\n",
		303: "HTTP/1.1 303 See Other\r\n",
		304: "HTTP/1.1 304 Not Modified\r\n",
		307: "HTTP/1.1 307 Temporary Redirect\r\n",
		308: "HTTP/1.1 308 Permanent Redirect\r\n",
		400: "HTTP/1.1 400 Bad Request\r\n",
		401: "HTTP/1.1 401 Unauthorized\r\n",
		402: "HTTP/1.1 402 Payment Required\r\n",
		403: "HTTP/1.1 403 Forbidden\r\n",
		404: "HTTP/1.1 404 Not Found\r\n",
		405: "HTTP/1.1 405 Method Not Allowed\r\n",
		406: "HTTP/1.1 406 Not Acceptable\r\n",
		407: "HTTP/1.1 407 Proxy Authentication Required\r\n",
		408: "HTTP/1.1 408 Request Timeout\r\n",
		409: "HTTP/1.1 409 Conflict\r\n",
		410: "HTTP/1.1 410 Gone\r\n",
		411: "HTTP/1.1 411 Length Required\r\n",
		412: "HTTP/1.1 412 Precondition Failed\r\n",
		413: "HTTP/1.1 413 Content Too Large\r\n",
		414: "HTTP/1.1 414 URI Too Long\r\n",
		415: "HTTP/1.1 415 Unsupported Media Type\r\n",
		416: "HTTP/1.1 416 Range Not Satisfiable\r\n",
		417: "HTTP/1.1 417 Expectation Failed\r\n",
		421: "HTTP/1.1 421 Misdirected Request\r\n",
		422: "HTTP/1.1 422 Unprocessable Content\r\n",
		426: "HTTP/1.1 426 Upgrade Required\r\n",
		428: "HTTP/1.1 428 Precondition Required\r\n",
		429: "HTTP/1.1 429 Too Many Requests\r\n",
		431: "HTTP/1.1 431 Request Header Fields Too Large\r\n",
		500: "HTTP/1.1 500 Internal Server Error\r\n",
		501: "HTTP/1.1 501 Not Implemented\r\n",
		502: "HTTP/1.1 502 Bad Gateway\r\n",
		503: "HTTP/1.1 503 Service Unavailable\r\n",
		504: "HTTP/1.1 504 Gateway Timeout\r\n",
		505: "HTTP/1.1 505 HTTP Version Not Supported\r\n",
	}

	// refusalAnswers is the whole answer to each refusal, indexed by Refusal:
	// its status line, no body, and the connection closed. RefusalNone's slot
	// holds the 400, which AppendRefusal also gives a value past the table.
	refusalAnswers = refusalAnswerTable()

	// methodHead is the one method whose answer carries no body.
	methodHead = []byte("HEAD")
)

// refusalAnswerTable spells, once, the answer to every refusal.
func refusalAnswerTable() [refusalCount]string {
	var t [refusalCount]string
	//: one string per refusal, from its status's line.
	for r := range refusalCount {
		status := max(refusalStatus[r], statusBadRequest)
		t[r] = statusLines[status] + "Content-Length: 0\r\nConnection: close\r\n\r\n"
	}
	//: the table.
	return t
}

// AppendStatusLine appends the status line of status to dst: a registered
// code's line is copied whole, any other code from 200 to 999 gets an empty
// reason phrase, which RFC 9112 §4 allows. A code below 200 is refused with
// corenet.HTTP1ResponseInvalid: the profile sends no interim response. It
// allocates nothing when dst has room.
func AppendStatusLine(dst []byte, status int) ([]byte, error) {
	//: three digits, and no interim response.
	if status < statusMin || status > statusMax {
		//: refused before a byte is appended.
		return dst, corenet.HTTP1ResponseInvalid
	}
	//: the precomputed line.
	if status < statusRegistered && statusLines[status] != "" {
		//: copied whole.
		return append(dst, statusLines[status]...), nil
	}
	//: an unregistered code: version, code, an empty reason.
	dst = append(dst, "HTTP/1.1 "...)
	dst = strconv.AppendInt(dst, int64(status), decimal)
	//: the empty reason and the line's end.
	return append(dst, " \r\n"...), nil
}

// AppendField appends the field line "name: value\r\n" to dst. A name that is
// not a token, a value with a control byte other than the tab, and the names
// the codec writes itself — Content-Length, Connection and Transfer-Encoding —
// are refused with corenet.HTTP1ResponseInvalid, and nothing is appended. It
// allocates nothing when dst has room.
func AppendField(dst []byte, name, value string) ([]byte, error) {
	//: a token name, never one of the framing fields, and a value a line can carry.
	if !token(name) || owned(name) || !fieldText(value) {
		//: refused.
		return dst, corenet.HTTP1ResponseInvalid
	}
	dst = append(dst, name...)
	dst = append(dst, ": "...)
	dst = append(dst, value...)
	//: the line's end.
	return append(dst, "\r\n"...), nil
}

// token reports whether s is a non-empty RFC 9110 token.
func token(s string) bool {
	//: an empty name is no token.
	if s == "" {
		//: refused.
		return false
	}
	//: every byte a tchar.
	for k := range len(s) {
		//: one byte outside tchar.
		if !tchar[s[k]] {
			//: refused.
			return false
		}
	}
	//: a token.
	return true
}

// fieldText reports whether s holds no control byte but the tab, and no DEL.
func fieldText(s string) bool {
	//: every byte checked.
	for k := range len(s) {
		c := s[k]
		//: C0 controls but the tab, and DEL.
		if (c < asciiSpace && c != '\t') || c == asciiDEL {
			//: refused.
			return false
		}
	}
	//: carriable.
	return true
}

// owned reports whether name is a field the codec writes itself.
func owned(name string) bool {
	//: the framing fields, in any case.
	return foldIs(name, "content-length") || foldIs(name, "connection") || foldIs(name, "transfer-encoding")
}

// AppendResponse appends the whole response res answers req with: the status
// line, res.Header, Content-Length and, when res.Close, req.Close or closing
// asks for it, Connection: close, then the body. With res.Stream the response
// is delimited by the connection's close: Connection: close and no
// Content-Length, the body being what the handler sends next; a Stream of a HEAD,
// a 204 or a 304 is refused.
//
// Content-Length is len(res.Body), so the response's framing is the codec's.
// The answer to a HEAD carries the Content-Length and no body. A 204 or a 304
// carries neither, and refuses a body. A status the profile does not send is
// refused with corenet.HTTP1ResponseInvalid, before anything is appended.
// res.Header is copied as it is: it is the handler's, written by AppendField.
// It allocates nothing when dst has room.
func AppendResponse(dst []byte, req *corenet.HTTP1RequestValue, res *corenet.HTTP1ResponseValue, closing bool) ([]byte, error) {
	//: a protocol switch: the status line only this answer writes.
	if res.Upgrade {
		//: 101, no length and no body.
		return appendUpgrade(dst, res)
	}
	status := orDefault(res.Status, statusOK)
	//: 204 and 304 never carry content (RFC 9110 §15.3.5, §15.4.5).
	bodiless := status == statusNoContent || status == statusNotModified
	//: a body the status may not carry, or a stream of an answer with no body.
	if refusedBody(res, req, bodiless) {
		//: refused before a byte is appended.
		return dst, corenet.HTTP1ResponseInvalid
	}
	out, err := AppendStatusLine(dst, status)
	//: an invalid status appends nothing.
	if err != nil {
		//: as AppendStatusLine refused it.
		return dst, err
	}
	out = append(out, res.Header...)
	out = appendLinkFields(out, res)
	out = appendFraming(out, bodyLength(res), bodiless || res.Stream, closing || res.Close || req.Close || res.Stream)
	//: a HEAD answer describes the body it does not send.
	if bytes.Equal(req.Method, methodHead) {
		//: no body.
		return out, nil
	}
	//: the content.
	return append(out, res.Body...), nil
}

// appendUpgrade appends a 101: the status line and Connection: Upgrade, then the
// protocol's own fields, which the handler wrote into Header.
func appendUpgrade(dst []byte, res *corenet.HTTP1ResponseValue) ([]byte, error) {
	//: a switch carries no body and no other status.
	if len(res.Body) > 0 || (res.Status != 0 && res.Status != statusSwitching) {
		//: refused before a byte is appended.
		return dst, corenet.HTTP1ResponseInvalid
	}
	dst = append(dst, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\n"...)
	dst = append(dst, res.Header...)
	//: the head's end, and nothing after it from this answer.
	return append(dst, "\r\n"...), nil
}

// refusedBody reports a response the status or the method cannot carry: a body
// on a 204 or 304, or a Stream of a HEAD or of a 204 or 304.
func refusedBody(res *corenet.HTTP1ResponseValue, req *corenet.HTTP1RequestValue, bodiless bool) bool {
	//: a file is a body too, and cannot share an answer with one.
	if res.File.R != nil && (bodiless || res.Stream || len(res.Body) > 0) {
		//: refused.
		return true
	}
	//: both answers have no body to hold.
	return (bodiless && len(res.Body) > 0) || (res.Stream && (bodiless || bytes.Equal(req.Method, methodHead)))
}

// bodyLength is the answer's Content-Length: the file's when the body is one,
// else the Body's.
func bodyLength(res *corenet.HTTP1ResponseValue) int64 {
	//: a file body.
	if res.File.R != nil {
		//: its length.
		return res.File.Len
	}
	//: the bytes the handler gave.
	return int64(len(res.Body))
}

// appendLinkFields appends what the chain's links left the codec to write: the
// CORS fields, a refusal's delay and the request's identifier, in that order.
func appendLinkFields(dst []byte, res *corenet.HTTP1ResponseValue) []byte {
	dst = append(dst, res.CORS...)
	//: a refusal's own delay, in seconds.
	if res.RetryAfter > 0 {
		dst = appendRetryAfter(dst, res.RetryAfter)
	}
	//: the one field written per request: a value, so it needs no slice.
	if id := res.RequestID.Bytes(); len(id) > 0 {
		dst = appendRequestID(dst, id)
	}
	//: as many as were set.
	return dst
}

// appendFraming appends Content-Length unless the status forbids it,
// Connection: close when asked, and the blank line that ends the head.
func appendFraming(dst []byte, n int64, bodiless, closing bool) []byte {
	//: the length, unless the status forbids it.
	if !bodiless {
		dst = append(dst, "Content-Length: "...)
		dst = strconv.AppendInt(dst, n, decimal)
		dst = append(dst, "\r\n"...)
	}
	//: any party may end the connection.
	if closing {
		dst = append(dst, "Connection: close\r\n"...)
	}
	//: the head's end.
	return append(dst, "\r\n"...)
}

// AppendRefusal appends the whole answer to r: its status line,
// Content-Length: 0 and Connection: close. The caller closes the connection
// once it is written; nothing a handler does changes it. RefusalNone, and a
// value this package does not return, append a 400. It allocates nothing
// when dst has room.
func AppendRefusal(dst []byte, r Refusal) []byte {
	//: a value past the table answers as a malformed request.
	if int(r) >= refusalCount {
		//: 400.
		return append(dst, refusalAnswers[RefusalNone]...)
	}
	//: the precomputed answer.
	return append(dst, refusalAnswers[r]...)
}

// appendInternalError appends the whole answer to a response the writer refused
// or a handler left deferred: a 500 with no body, the connection closed, and
// the request's identifier when a link gave it one, so even this answer can be
// joined to its access record.
func appendInternalError(dst []byte, res *corenet.HTTP1ResponseValue) []byte {
	id := res.RequestID.Bytes()
	//: the common case of an unidentified answer: the precomputed bytes.
	if len(id) == 0 {
		//: as ever.
		return append(dst, internalError...)
	}
	dst = append(dst, statusLines[statusInternal]...)
	dst = appendRequestID(dst, id)
	//: the rest of the head.
	return append(dst, internalErrorTail...)
}

// appendRequestID appends the X-Request-Id field line of id.
func appendRequestID(dst, id []byte) []byte {
	dst = append(dst, "X-Request-Id: "...)
	dst = append(dst, id...)
	//: the line's end.
	return append(dst, "\r\n"...)
}

// appendRetryAfter appends the Retry-After field line of seconds.
func appendRetryAfter(dst []byte, seconds int) []byte {
	dst = append(dst, "Retry-After: "...)
	dst = strconv.AppendInt(dst, int64(seconds), decimal)
	//: the line's end.
	return append(dst, "\r\n"...)
}
