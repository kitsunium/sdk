// Package kit — the errors a product returns to its callers, and their wire
// mapping.
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

// The framework's own failures carry SDK error codes in 0.4.2.* — layer 4 is
// the framework module, PP 2 this package (ADR 0147 §3) — so a product keeps
// the whole application range 0x40–0x7F for its own codes. Match them with
// errs.HasCode. Their Public text is the only part that may reach a caller;
// none of them is a 4xx, so a caller only ever reads "internal error".
const (
	CodeStoreDecode  errs.Code = 0x00_04_02_01 // 0.4.2.1 — a stored entity no longer decodes into its type
	CodeStoreEncode  errs.Code = 0x00_04_02_02 // 0.4.2.2 — an entity does not encode to JSON
	CodeStorePersist errs.Code = 0x00_04_02_03 // 0.4.2.3 — the store file could not be written
	CodeStoreLoad    errs.Code = 0x00_04_02_04 // 0.4.2.4 — the store file could not be read at start
	CodeStoreIndex   errs.Code = 0x00_04_02_05 // 0.4.2.5 — the stored entities break a unique index, or a key function panicked
	CodeTopicEncode  errs.Code = 0x00_04_02_06 // 0.4.2.6 — a message does not encode to JSON
	CodeTopicPublish errs.Code = 0x00_04_02_07 // 0.4.2.7 — a subscription's queue refused a message
	CodeQueueOpen    errs.Code = 0x00_04_02_08 // 0.4.2.8 — a subscription's queue could not be opened
	CodeUndecodable  errs.Code = 0x00_04_02_09 // 0.4.2.9 — a delivered message does not decode
	CodeHandlerPanic errs.Code = 0x00_04_02_0A // 0.4.2.10 — a subscription handler panicked
	CodeNotMounted   errs.Code = 0x00_04_02_0B // 0.4.2.11 — a subscription's topic is not mounted in the app
	CodeWorkflowLoad errs.Code = 0x00_04_02_0C // 0.4.2.12 — a workflow's bookkeeping could not be read at start
	// A workflow hook panicked: an OnEnter hook fails its transition with it,
	// an OnTransition hook is reported and the transition stands.
	CodeWorkflowHookPanic errs.Code = 0x00_04_02_0D // 0.4.2.13
	// An OnEnter hook changed the state its entity was entering, or the
	// entity's key: the transition is refused.
	CodeWorkflowHookChange errs.Code = 0x00_04_02_0E // 0.4.2.14
	// An OnEnter hook fired its own workflow: refused, where it would wait
	// for itself.
	CodeWorkflowReentrant errs.Code = 0x00_04_02_0F // 0.4.2.15
	CodeAppConfig         errs.Code = 0x00_04_02_10 // 0.4.2.16 — the app's configuration cannot be applied
	CodeAppRunning        errs.Code = 0x00_04_02_11 // 0.4.2.17 — the app, or one of its services, is already running
	CodeAppListen         errs.Code = 0x00_04_02_12 // 0.4.2.18 — the app could not listen on its address

	// Mailers.
	CodeMailConfig errs.Code = 0x00_04_02_13 // 0.4.2.19 — KIT_SMTP_URL cannot be used
	CodeMailEncode errs.Code = 0x00_04_02_14 // 0.4.2.20 — a mail does not encode for the outbox
	CodeMailQueue  errs.Code = 0x00_04_02_15 // 0.4.2.21 — the outbox could not be opened, or refused a mail
	// Deprecated: kit no longer returns it. The outbox is the SDK's mail
	// spool, which dead-letters a record that does not decode itself
	// (mail.SpooledMailUndecodable).
	CodeMailUndecodable errs.Code = 0x00_04_02_16 // 0.4.2.22
	CodeMailPanic       errs.Code = 0x00_04_02_17 // 0.4.2.23 — a delivery attempt panicked

	// Loops.
	CodeLoopPanic      errs.Code = 0x00_04_02_18 // 0.4.2.24 — a loop's function panicked
	CodeLoopNotMounted errs.Code = 0x00_04_02_19 // 0.4.2.25 — a loop wakes on a topic whose service is not mounted in the app
	CodeLoopStart      errs.Code = 0x00_04_02_1A // 0.4.2.26 — a hand-written loop's supervisor refused to start

	// Secrets.
	CodeSecretStore   errs.Code = 0x00_04_02_1B // 0.4.2.27 — the environment's secret store cannot be used: KIT_SECRETS, KIT_SECRETS_KEY
	CodeSecretMissing errs.Code = 0x00_04_02_1C // 0.4.2.28 — a declared secret is found nowhere
	CodeSecretRead    errs.Code = 0x00_04_02_1D // 0.4.2.29 — a secret's store refused to read it
	CodeSecretRotate  errs.Code = 0x00_04_02_1E // 0.4.2.30 — a generated secret could not be made or rotated

	// Databases, ADR 0004.
	CodeDatabaseOpen        errs.Code = 0x00_04_02_1F // 0.4.2.31 — a database's engine could not open its pool
	CodeDatabaseMigrate     errs.Code = 0x00_04_02_20 // 0.4.2.32 — a database's migrations did not apply, or wait for `migrate up`
	CodeDatabaseUnavailable errs.Code = 0x00_04_02_21 // 0.4.2.33 — a database did not answer
	CodeDatabaseConfig      errs.Code = 0x00_04_02_22 // 0.4.2.34 — a database's URL or tuning cannot be used
	// A transaction wrote a store another database keeps than the one it
	// belongs to — the data directory counting as one: refused, Invalid.
	CodeTransactionSpan errs.Code = 0x00_04_02_42 // 0.4.2.66
	// Privacy (ADR 0006).
	CodePrivacyKey     errs.Code = 0x00_04_02_23 // 0.4.2.35 — kit's index key cannot be read, made or derived
	CodePrivacyErase   errs.Code = 0x00_04_02_24 // 0.4.2.36 — an erasure failed: the store's Anonymise function panicked
	CodePrivacyJournal errs.Code = 0x00_04_02_25 // 0.4.2.37 — the privacy journal cannot be read or written
	CodePrivacyData    errs.Code = 0x00_04_02_26 // 0.4.2.38 — the privacy command cannot open the data: the product runs, or keeps it in memory
	// History (ADR 0007).
	CodeHistoryLoad  errs.Code = 0x00_04_02_27 // 0.4.2.39 — a store's history cannot be opened at start
	CodeHistoryWrite errs.Code = 0x00_04_02_28 // 0.4.2.40 — a store's history cannot be written: the record's write is refused with it
	CodeHistoryRead  errs.Code = 0x00_04_02_29 // 0.4.2.41 — a store's history cannot be read
	CodePasswordHash errs.Code = 0x00_04_02_2A // 0.4.2.42 — a password cannot be hashed, or a stored hash cannot be read
	// Commands and queries.
	CodeCommandReentrant errs.Code = 0x00_04_02_2B // 0.4.2.43 — a handler dispatched its own command with its own key: refused, never a deadlock
	CodeCommandKey       errs.Code = 0x00_04_02_2C // 0.4.2.44 — a command's key could not be held
	CodeCommandQueue     errs.Code = 0x00_04_02_2D // 0.4.2.45 — a queued command's queue could not be opened, or refused a dispatch
	CodeCommandEncode    errs.Code = 0x00_04_02_2E // 0.4.2.46 — a queued command's input does not encode for its queue
	// Watches (ADR 0008).
	CodeWatchQueue errs.Code = 0x00_04_02_2F // 0.4.2.47 — a watch's queue could not be opened, or refused a notice: the write stands, its notice is lost
	// Sealing at rest (ADR 0006, step 3).
	CodeSealKey    errs.Code = 0x00_04_02_3D // 0.4.2.61 — kit's data keys cannot be reached: data-key, or kit's own store of data keys
	CodeSealWrite  errs.Code = 0x00_04_02_3E // 0.4.2.62 — a member could not be sealed: the write is refused, nothing changed
	CodeSealOpen   errs.Code = 0x00_04_02_3F // 0.4.2.63 — a sealed member does not open: altered, moved, or its data key does not unwrap
	CodeSealRewrap errs.Code = 0x00_04_02_40 // 0.4.2.64 — data-key's rotation could not re-wrap every data key: the old version is kept
	CodeSealShred  errs.Code = 0x00_04_02_41 // 0.4.2.65 — an erasure could not destroy a data key: the erasure's rewrite stands
	// Revisions (ADR 0007 §3).
	CodeRevisionRead   errs.Code = 0x00_04_02_43 // 0.4.2.67 — a record's versions cannot be read, or two of them compared
	CodeRevisionWrite  errs.Code = 0x00_04_02_44 // 0.4.2.68 — a record's versions cannot be rewritten: an erasure's, a hold's, a rollback's
	CodeRevisionDecode errs.Code = 0x00_04_02_45 // 0.4.2.69 — a version no longer decodes into the store's type: Revisions, Revision and Restore refuse it

	// The framework's own signals and refusals, typed since the move into the
	// SDK (rule 2: no fmt.Errorf, no errors.New in production code).
	CodeJobOverlapped  errs.Code = 0x00_04_02_31 // 0.4.2.49 — a scheduled job's run was skipped: the previous one is still going
	CodeHistoryMoved   errs.Code = 0x00_04_02_32 // 0.4.2.50 — an undo ended: another write changed the history since
	CodeNotSource      errs.Code = 0x00_04_02_33 // 0.4.2.51 — the source endpoint's name holds no regular file
	CodePasswordMoved  errs.Code = 0x00_04_02_34 // 0.4.2.52 — a password write ended: another write changed the hash since
	CodeNothingToErase errs.Code = 0x00_04_02_35 // 0.4.2.53 — an erasure's update found nothing to erase
	CodeKeyRekeyed     errs.Code = 0x00_04_02_36 // 0.4.2.54 — an erasure's update would change a key that depends on personal data
	CodeMigrateRefused errs.Code = 0x00_04_02_37 // 0.4.2.55 — the migrate command cannot do what it was asked
	CodeCatalogue      errs.Code = 0x00_04_02_38 // 0.4.2.56 — the framework's own message catalogues disagree
	CodeRetentionPanic errs.Code = 0x00_04_02_39 // 0.4.2.57 — a store's retention function panicked
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

// Error is an error a product returns to its callers. Message travels on the
// wire; the cause attached with [Error.Wrap] is logged and never sent.
//
// Return one from a handler to choose the HTTP status and the message the
// caller reads. Any other error becomes a 500 whose body says nothing about
// it: an error message is the classic place a secret leaks from.
type Error struct {
	// Status is the HTTP status.
	Status int
	// Code is a stable, snake_case identifier a client can branch on.
	Code string
	// Message is wire-safe text for the caller.
	Message string
	// Violations detail an invalid request, one per failed rule.
	Violations []ViolationMessage

	cause error
}

// ViolationMessage is one validation rule a request failed: where, which rule, and
// why — never the value.
type ViolationMessage struct {
	// Path locates the value: "title", "items[2].zip".
	Path string `json:"path"`
	// Rule names the failed rule: "required", "maxlen".
	Rule string `json:"rule"`
	// Message explains it without echoing the value.
	Message string `json:"message"`
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

// Unwrap returns the cause, so errors.Is and errors.As see through an Error.
func (e *Error) Unwrap() error { return e.cause }

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
