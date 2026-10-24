package http1

import (
	corenet "github.com/kitsunium/sdk/internal/core/net"
)

const (
	// RefusalNone is no refusal: the request was framed, or needs more bytes.
	RefusalNone Refusal = iota
	// RefusalRequestLine is a request line that does not end right after its
	// version.
	RefusalRequestLine
	// RefusalMethod is an empty method, or one that is not a token.
	RefusalMethod
	// RefusalTarget is an empty request-target, or one holding a control byte,
	// DEL or a byte from 0x80 up.
	RefusalTarget
	// RefusalVersion is any version but HTTP/1.1.
	RefusalVersion
	// RefusalBareCR is a CR not followed by LF.
	RefusalBareCR
	// RefusalBareLF is a LF not preceded by CR.
	RefusalBareLF
	// RefusalNUL is a NUL inside a field value or name.
	RefusalNUL
	// RefusalControl is a control byte other than the tab, or DEL, inside a
	// field value.
	RefusalControl
	// RefusalObsFold is a field line that starts with a space or a tab: the
	// obsolete line folding (RFC 9112 §5.2).
	RefusalObsFold
	// RefusalSpaceBeforeColon is whitespace between a field name and its colon
	// (RFC 9112 §5.1).
	RefusalSpaceBeforeColon
	// RefusalName is an empty field name, one that is not a token, or a field
	// line with no colon.
	RefusalName
	// RefusalTransferEncoding is any Transfer-Encoding field, chunked or not:
	// the profile frames by Content-Length alone, so it carries no chunked body
	// and no trailer.
	RefusalTransferEncoding
	// RefusalContentLength is a Content-Length that is empty, not decimal
	// digits, or longer than corenet.HTTP1MaxContentLengthDigits.
	RefusalContentLength
	// RefusalContentLengthDuplicated is a second Content-Length field, equal to
	// the first or not.
	RefusalContentLengthDuplicated
	// RefusalAuthorizationDuplicated is a second Authorization field.
	RefusalAuthorizationDuplicated
	// RefusalHostDuplicated is a second Host field.
	RefusalHostDuplicated
	// RefusalExpect is an Expect field: the profile sends no interim response,
	// so 100-continue cannot be met (RFC 9110 §10.1.1).
	RefusalExpect
	// RefusalHeadTooLarge is a head that does not end within the head bound.
	RefusalHeadTooLarge
	// RefusalTooManyFields is a field line past corenet.HTTP1MaxFields.
	RefusalTooManyFields
	// RefusalTargetTooLong is a request-target past the target bound.
	RefusalTargetTooLong
	// RefusalBodyTooLarge is a Content-Length past the body bound.
	RefusalBodyTooLarge
	// RefusalMethodUnsupported is CONNECT: the profile opens no tunnel.
	RefusalMethodUnsupported
)

// refusalCount is one past the last Refusal: the length of every table below.
const refusalCount int = int(RefusalMethodUnsupported) + 1

// The status codes a refusal is answered with (RFC 9110 §15).
const (
	// statusBadRequest answers every syntax and framing refusal.
	statusBadRequest int = 400
	// statusContentTooLarge answers a body past its bound.
	statusContentTooLarge int = 413
	// statusURITooLong answers a target past its bound.
	statusURITooLong int = 414
	// statusExpectationFailed answers an Expect field.
	statusExpectationFailed int = 417
	// statusFieldsTooLarge answers a head past its bound.
	statusFieldsTooLarge int = 431
	// statusNotImplemented answers CONNECT.
	statusNotImplemented int = 501
)

var (
	// refusalNames are the names String renders, indexed by Refusal.
	refusalNames = [refusalCount]string{
		RefusalNone:                    "none",
		RefusalRequestLine:             "request_line",
		RefusalMethod:                  "method",
		RefusalTarget:                  "target",
		RefusalVersion:                 "version",
		RefusalBareCR:                  "bare_cr",
		RefusalBareLF:                  "bare_lf",
		RefusalNUL:                     "nul",
		RefusalControl:                 "control",
		RefusalObsFold:                 "obs_fold",
		RefusalSpaceBeforeColon:        "space_before_colon",
		RefusalName:                    "name",
		RefusalTransferEncoding:        "transfer_encoding",
		RefusalContentLength:           "content_length",
		RefusalContentLengthDuplicated: "content_length_duplicated",
		RefusalAuthorizationDuplicated: "authorization_duplicated",
		RefusalHostDuplicated:          "host_duplicated",
		RefusalExpect:                  "expect",
		RefusalHeadTooLarge:            "head_too_large",
		RefusalTooManyFields:           "too_many_fields",
		RefusalTargetTooLong:           "target_too_long",
		RefusalBodyTooLarge:            "body_too_large",
		RefusalMethodUnsupported:       "method_unsupported",
	}

	// refusalStatus is the status each refusal is answered with; RefusalNone's
	// is zero.
	refusalStatus = [refusalCount]int{
		RefusalRequestLine:             statusBadRequest,
		RefusalMethod:                  statusBadRequest,
		RefusalTarget:                  statusBadRequest,
		RefusalVersion:                 statusBadRequest,
		RefusalBareCR:                  statusBadRequest,
		RefusalBareLF:                  statusBadRequest,
		RefusalNUL:                     statusBadRequest,
		RefusalControl:                 statusBadRequest,
		RefusalObsFold:                 statusBadRequest,
		RefusalSpaceBeforeColon:        statusBadRequest,
		RefusalName:                    statusBadRequest,
		RefusalTransferEncoding:        statusBadRequest,
		RefusalContentLength:           statusBadRequest,
		RefusalContentLengthDuplicated: statusBadRequest,
		RefusalAuthorizationDuplicated: statusBadRequest,
		RefusalHostDuplicated:          statusBadRequest,
		RefusalExpect:                  statusExpectationFailed,
		RefusalHeadTooLarge:            statusFieldsTooLarge,
		RefusalTooManyFields:           statusFieldsTooLarge,
		RefusalTargetTooLong:           statusURITooLong,
		RefusalBodyTooLarge:            statusContentTooLarge,
		RefusalMethodUnsupported:       statusNotImplemented,
	}

	// refusalErrs is the core/net sentinel each refusal belongs to, grouped by
	// the decision a caller makes rather than by the byte that broke.
	refusalErrs = [refusalCount]error{
		RefusalRequestLine:             corenet.HTTP1RequestMalformed,
		RefusalMethod:                  corenet.HTTP1RequestMalformed,
		RefusalTarget:                  corenet.HTTP1RequestMalformed,
		RefusalVersion:                 corenet.HTTP1RequestMalformed,
		RefusalBareCR:                  corenet.HTTP1RequestMalformed,
		RefusalBareLF:                  corenet.HTTP1RequestMalformed,
		RefusalNUL:                     corenet.HTTP1RequestMalformed,
		RefusalControl:                 corenet.HTTP1RequestMalformed,
		RefusalObsFold:                 corenet.HTTP1RequestMalformed,
		RefusalSpaceBeforeColon:        corenet.HTTP1RequestMalformed,
		RefusalName:                    corenet.HTTP1RequestMalformed,
		RefusalTransferEncoding:        corenet.HTTP1TransferEncoding,
		RefusalContentLength:           corenet.HTTP1ContentLengthInvalid,
		RefusalContentLengthDuplicated: corenet.HTTP1ContentLengthInvalid,
		RefusalAuthorizationDuplicated: corenet.HTTP1FieldDuplicated,
		RefusalHostDuplicated:          corenet.HTTP1FieldDuplicated,
		RefusalExpect:                  corenet.HTTP1ExpectRefused,
		RefusalHeadTooLarge:            corenet.HTTP1HeadTooLarge,
		RefusalTooManyFields:           corenet.HTTP1HeadTooLarge,
		RefusalTargetTooLong:           corenet.HTTP1TargetTooLong,
		RefusalBodyTooLarge:            corenet.HTTP1BodyTooLarge,
		RefusalMethodUnsupported:       corenet.HTTP1MethodUnsupported,
	}
)

// Refusal names the rule a request broke. The zero Refusal, RefusalNone, is
// no refusal at all. Every other one is answered with its Status, Connection:
// close, and the connection closed: the request never reaches a handler, and a
// handler has no say in it.
type Refusal uint8

// String names the refusal for a log line or a metric label.
func (r Refusal) String() string {
	//: a value past the table is no Refusal this package returns.
	if int(r) >= refusalCount {
		//: never a panic on a caller's cast.
		return "unknown"
	}
	//: the table's name.
	return refusalNames[r]
}

// Status returns the status code the refusal is answered with: 400 for a
// malformed or smuggling-prone request, 413, 414 and 431 for the size bounds,
// 417 for Expect and 501 for CONNECT. RefusalNone, and a value this package
// does not return, give 0.
func (r Refusal) Status() int {
	//: a value past the table is no Refusal this package returns.
	if int(r) >= refusalCount {
		//: nothing to answer.
		return 0
	}
	//: the table's status.
	return refusalStatus[r]
}

// Err returns the core/net sentinel the refusal belongs to, or nil for
// RefusalNone. Several refusals share a sentinel: the sentinel is what a caller
// branches on, the Refusal is what a log line names.
func (r Refusal) Err() error {
	//: a value past the table is no Refusal this package returns.
	if int(r) >= refusalCount {
		//: no error.
		return nil
	}
	//: the table's sentinel, preallocated.
	return refusalErrs[r]
}
