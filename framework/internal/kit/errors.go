package kit

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// clipLimit is how many characters of caller-controlled text a message
// quotes.
const clipLimit int = 64

// The wire codes a caller branches on: an [Error]'s Code, snake_case, kept
// byte-for-byte from the platform's kit (ADR 0147 §6). They are named Wire*,
// not Code*, because Code* names a dotted-quad errs.Code in the SDK and the
// registry audit holds every Code* constant to that.
const (
	WireInvalid     = "invalid_argument"
	WireNotFound    = "not_found"
	WireConflict    = "conflict"
	WireUnauth      = "unauthenticated"
	WireForbidden   = "permission_denied"
	WireTooLarge    = "too_large"
	WireUnsupported = "unsupported_media_type"
	WireRateLimited = "rate_limited"
	WireUnavailable = "unavailable"
	WireTimeout     = "deadline_exceeded"
	WireCanceled    = "canceled"
	WireInternal    = "internal"
)

// sdkCodes names on the wire the SDK refusals kit's mechanics produce. The
// SDK says their status (errs.HTTPStatusOf: 429, 503, 504); kit says their
// code — and that a 503 or a 504 from them is safe to explain, where any
// other 5xx is "internal error".
var sdkCodes = map[string]string{
	"RATE_LIMITED":     WireRateLimited,
	"BULKHEAD_FULL":    WireUnavailable,
	"CIRCUIT_OPEN":     WireUnavailable,
	"TIMEOUT_EXCEEDED": WireTimeout,
}

// wireError is what a caller receives for any error.
type wireError struct {
	Error wireBody `json:"error"`
}

type wireBody struct {
	Code       string             `json:"code"`
	Message    string             `json:"message"`
	Violations []ViolationMessage `json:"violations,omitempty"`
}

// Error renders the code and the message. It never renders the cause.
func (e *Error) Error() string { return e.Code + ": " + e.Message }

// unwrap is Error.Unwrap's body: decl_gen.go writes Error.Unwrap, from the
// design, as one call of it.
func (e *Error) unwrap() error { return e.cause }

// Wrap attaches the underlying cause, for the logs. It returns a copy.
func (e *Error) Wrap(cause error) *Error {
	c := *e
	c.cause = cause
	return &c
}

// Invalid reports a request the caller must change before retrying (400).
func Invalid(message string) *Error {
	return NewError(http.StatusBadRequest, WireInvalid, message)
}

// NotFound reports that the addressed resource does not exist (404).
func NotFound(message string) *Error {
	return NewError(http.StatusNotFound, WireNotFound, message)
}

// Conflict reports a request the resource's current state refuses (409).
func Conflict(message string) *Error {
	return NewError(http.StatusConflict, WireConflict, message)
}

// Unauthenticated reports a caller who did not prove who they are (401).
func Unauthenticated(message string) *Error {
	return NewError(http.StatusUnauthorized, WireUnauth, message)
}

// Forbidden reports a caller who may not do this (403).
func Forbidden(message string) *Error {
	return NewError(http.StatusForbidden, WireForbidden, message)
}

// Unavailable reports a transient failure worth retrying later (503).
func Unavailable(message string) *Error {
	return NewError(http.StatusServiceUnavailable, WireUnavailable, message)
}

// failure builds one of kit's own errors: a code a caller can match on, a
// public sentence, and — for the logs only — what actually went wrong.
func failure(code errs.Code, reason, public string, cause error, fields ...errs.Field) error {
	if cause == nil {
		return errs.New(code, reason, public, public, fields...)
	}
	return errs.Wrap(cause, errs.WrapParams{Code: code, Reason: reason, Public: public, Private: cause.Error()}, fields...)
}

// explain builds one of kit's own errors whose public sentence stays kit's
// when the cause is an SDK error. failure's Wrap lets such a cause's own
// sentence win, which is right when the SDK knows best ("credentials cannot
// be sent over an unencrypted session") and wrong when only kit knows which
// variable or file it read. The cause's text goes to the logs; errors.Is no
// longer reaches it — test for what matters before explaining.
func explain(code errs.Code, reason, public string, cause error, fields ...errs.Field) error {
	private := public
	if cause != nil {
		private = cause.Error()
	}
	return errs.New(code, reason, public, private, fields...)
}

// clip bounds caller-controlled text a message quotes — a key from the URL,
// the name of an unknown member — so a response never reflects a megabyte.
func clip(s string) string {
	if utf8.RuneCountInString(s) <= clipLimit {
		return s
	}
	return string([]rune(s)[:clipLimit]) + "…"
}

// describe turns any error into what may be said about it on the wire: an
// HTTP status and a body. It is the one place that decision is taken, so a
// handler, the in-process call path and the live event stream agree.
func describe(err error) (int, wireBody) {
	if ke, ok := errors.AsType[*Error](err); ok {
		return ke.Status, wireBody{Code: ke.Code, Message: ke.Message, Violations: ke.Violations}
	}
	if tooLarge, ok := errors.AsType[*http.MaxBytesError](err); ok && tooLarge != nil {
		return http.StatusRequestEntityTooLarge, wireBody{Code: WireTooLarge, Message: "the request body is too large"}
	}
	if reason, ok := errs.ReasonOf(err); ok {
		return describeSDK(err, reason)
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout, wireBody{Code: WireTimeout, Message: "the deadline was exceeded"}
	case errors.Is(err, context.Canceled):
		return http.StatusServiceUnavailable, wireBody{Code: WireCanceled, Message: "the request was canceled"}
	default:
		return http.StatusInternalServerError, wireBody{Code: WireInternal, Message: "internal error"}
	}
}

// describeSDK is describe of an SDK error whose reason is reason: kit's code
// for a reason it maps, the reason itself otherwise — and never more than
// "internal error" for a server's failure.
func describeSDK(err error, reason string) (int, wireBody) {
	status := errs.HTTPStatusOf(err)
	if code, known := sdkCodes[reason]; known {
		return status, wireBody{Code: code, Message: errs.PublicOf(err)}
	}
	if status >= http.StatusInternalServerError {
		return status, wireBody{Code: WireInternal, Message: "internal error"}
	}
	return status, wireBody{Code: strings.ToLower(reason), Message: errs.PublicOf(err)}
}

// NewError is an error answered with the HTTP status, carrying the wire code
// and the message a caller reads.
func NewError(status int, code, message string) *Error {
	return &Error{Status: status, Code: code, Message: message}
}
