// Package errs: error.go defines the SDK-wide typed Error plus the Define
// and Wrap constructors. Consumers never import this package directly —
// they introspect errors through github.com/kitsunium/sdk/pkg/v1/errs.
package errs

import (
	"errors"
	"slices"
	"strconv"
)

// defaultHTTPStatus is the wire status returned when an Error does not
// override it. 500 is deliberately generic — callers who want layer-aware
// mapping put the raw Code in a response header.
const defaultHTTPStatus int = 500

// defaultExitCode is the POSIX exit status returned when an Error does not
// override it. 70 matches sysexits EX_SOFTWARE (internal software error).
const defaultExitCode int = 70

// layerDivisor is the base used to extract the layer digit from a Code
// (Code / layerDivisor). A Code of 3101 yields layer 3.
const layerDivisor int = 1000

// minValidLayer is the smallest layer digit the SDK recognises (1 = kernel).
const minValidLayer int = 1

// maxValidLayer is the largest layer digit the SDK recognises today
// (4 = public facade). Further layers pushed via ADR 0002.
const maxValidLayer int = 9

// Error is the SDK-wide typed error. Fields are unexported — consumers
// obtain values via the getter methods on *Error or via the Of-accessors
// in github.com/kitsunium/sdk/pkg/v1/errs. Instances are immutable after
// construction: Wrap returns a new *Error, never mutates the input.
type Error struct {
	code           int
	reason         string
	public         string
	private        string
	fields         []FieldValue
	trail          []Code
	trailTruncated bool
	httpOverride   int
	exitOverride   int
	source         error
}

// DefineOption tunes an Error at Define time. Options compose left-to-right.
type DefineOption func(e *Error)

// WithHTTPStatus overrides the default HTTP status (500) for an Error.
//
// Params:
//   - status: HTTP response code the emitter wants associated with this Error.
//
// Returns:
//   - DefineOption: an opaque option applied by Define / Wrap.
func WithHTTPStatus(status int) (opt DefineOption) {
	//: return a closure so the option can be passed positionally to Define.
	return func(e *Error) {
		//: store as override so the zero-value still signals "use default".
		e.httpOverride = status
	}
}

// WithExitCode overrides the default POSIX exit code (70) for an Error.
//
// Params:
//   - code: sysexits-style exit status the emitter wants associated.
//
// Returns:
//   - DefineOption: an opaque option applied by Define / Wrap.
func WithExitCode(code int) (opt DefineOption) {
	//: closure mirrors WithHTTPStatus for symmetry at call sites.
	return func(e *Error) {
		//: store as override so zero keeps meaning "default".
		e.exitOverride = code
	}
}

// NewError is a tooling-friendly alias for Define. Prefer Define at call
// sites for consistency; NewError exists so code generators and lints
// expecting a NewXxx constructor on exported types find one.
//
// Params:
//   - code: layered numeric identifier (>= 1000, unique SDK-wide).
//   - reason: SCREAMING_SNAKE stable identifier (matches the enclosing var).
//   - public: wire-safe message; the AST audit requires a string literal.
//   - private: log-only detailed message; may reference internal concepts.
//   - opts: optional Define-time overrides (WithHTTPStatus, WithExitCode).
//
// Returns:
//   - *Error: delegation to Define, identical semantics.
func NewError(code int, reason, public, private string, opts ...DefineOption) (e *Error) {
	//: single source of truth lives in Define; NewError is a thin alias.
	return Define(code, reason, public, private, opts...)
}

// Define registers a sentinel-style *Error at package init. It panics
// (with a message citing one of the documentary meta-codes 1001/1002/1003)
// when validateDefineArgs returns non-nil, so structural mistakes surface
// at program start rather than at the first emission.
//
// Params:
//   - code: layered numeric identifier (>= 1000, unique SDK-wide).
//   - reason: SCREAMING_SNAKE stable identifier (matches the enclosing var).
//   - public: wire-safe message; the AST audit requires a string literal.
//   - private: log-only detailed message; may reference internal concepts.
//   - opts: optional Define-time overrides (WithHTTPStatus, WithExitCode).
//
// Returns:
//   - *Error: a fully constructed sentinel ready to be returned from APIs.
func Define(code int, reason, public, private string, opts ...DefineOption) (e *Error) {
	//: validate up-front so bad sentinels never reach runtime call sites.
	if err := validateDefineArgs(code, reason, public, private); err != nil {
		//: panic at init fails the binary — operators grep the meta-code.
		panic(err.Error())
	}
	//: construct the base sentinel; overrides apply after.
	out := &Error{code: code, reason: reason, public: public, private: private}
	//: apply every Define-time option left-to-right.
	for _, opt := range opts {
		//: each option mutates the fresh instance before it is returned.
		opt(out)
	}
	//: hand back the sentinel ready to be returned from APIs.
	return out
}

// Wrap attaches a cause to a new *Error with origin-wins semantics.
// When cause is already an *Error the returned *Error inherits its code,
// reason, public, and private (params are IGNORED on that path; only
// fields are appended). When cause is a stdlib error, params supply the
// new Error's values and the stdlib cause is preserved through Unwrap.
//
// Params:
//   - cause: original error being wrapped; may be nil.
//   - params: the code / reason / public / private used only on the stdlib path.
//   - fields: metadata always appended (both paths).
//
// Returns:
//   - *Error: a fresh *Error wrapping the cause; never mutates input.
func Wrap(cause error, params WrapParams, fields ...FieldValue) (e *Error) {
	//: detect the *Error-cause branch via errors.AsType — origin wins there.
	if inner, ok := errors.AsType[*Error](cause); ok {
		//: build the combined fields slice via slices.Concat for clarity.
		merged := slices.Concat(inner.fields, fields)
		//: inherit every semantic field from the origin *Error.
		return &Error{
			code:         inner.code,
			reason:       inner.reason,
			public:       inner.public,
			private:      inner.private,
			fields:       merged,
			httpOverride: inner.httpOverride,
			exitOverride: inner.exitOverride,
			source:       cause,
		}
	}
	//: stdlib-cause branch — validate the supplied params like Define would.
	if err := validateDefineArgs(params.Code, params.Reason, params.Public, params.Private); err != nil {
		//: panic: Wrap mis-use is a programming error just like a bad Define.
		panic(err.Error())
	}
	//: defensive copy of fields so callers cannot mutate post-hoc.
	copied := slices.Clone(fields)
	//: hand back a freshly constructed *Error wrapping the stdlib cause.
	return &Error{
		code:    params.Code,
		reason:  params.Reason,
		public:  params.Public,
		private: params.Private,
		fields:  copied,
		source:  cause,
	}
}

// Code returns this Error's numeric identifier.
//
// Returns:
//   - int: the layered code assigned at Define time.
func (e *Error) Code() (code int) {
	//: direct read of the immutable member.
	return e.code
}

// Reason returns this Error's stable SCREAMING_SNAKE identifier.
//
// Returns:
//   - string: the Reason assigned at Define time.
func (e *Error) Reason() (reason string) {
	//: direct read — Reason is part of the API contract.
	return e.reason
}

// Public returns this Error's wire-safe message.
//
// Returns:
//   - string: the literal Public message assigned at Define time.
func (e *Error) Public() (public string) {
	//: direct read — Public is wire-safe by construction.
	return e.public
}

// Private returns this Error's log-only detailed message. Never expose
// the return value over any user-facing surface.
//
// Returns:
//   - string: the Private message assigned at Define time.
func (e *Error) Private() (private string) {
	//: direct read — callers accept the diagnostic-only contract.
	return e.private
}

// Fields returns a defensive copy of the structured metadata attached to
// this Error. Mutating the returned slice has no effect on the Error.
//
// Returns:
//   - []FieldValue: a fresh slice holding every FieldValue recorded.
func (e *Error) Fields() (out []FieldValue) {
	//: copy on read so callers cannot mutate our internal state.
	return slices.Clone(e.fields)
}

// Layer returns the thousands digit of Code when it falls in the SDK's
// recognised layer range, otherwise zero to signal "unclassified".
//
// Returns:
//   - int: 1..9 for a valid layered code, 0 otherwise.
func (e *Error) Layer() (layer int) {
	//: integer division cheaply extracts the thousands digit.
	n := e.code / layerDivisor
	//: reject codes that do not fit the 1..9 layered convention.
	if n < minValidLayer || n > maxValidLayer {
		//: zero is the documented "unclassified" sentinel.
		return 0
	}
	//: hand back the well-formed layer digit.
	return n
}

// Error returns the neutral stable form "[<code> <REASON>] <public>". It
// never includes Private content nor Fields, so callers may safely bubble
// err.Error() across any boundary without leaking diagnostic data.
//
// Returns:
//   - string: the stable neutral representation.
func (e *Error) Error() (s string) {
	//: compose the three neutral parts directly — no fmt.Sprintf needed.
	return "[" + strconv.Itoa(e.code) + " " + e.reason + "] " + e.public
}

// Source returns the wrapped cause attached by Wrap.
//
// Returns:
//   - error: the cause attached at Wrap time; nil for Define-only sentinels.
func (e *Error) Source() (err error) {
	//: direct read — sentinels created by Define have no cause.
	return e.source
}

// Unwrap delegates to Source so the stdlib errors.Is / errors.As machinery
// can walk the chain. The nil-guard also makes this slightly more than a
// pure field getter, which matters for callers storing errors by value.
//
// Returns:
//   - error: the cause attached at Wrap time; nil when the receiver is nil.
func (e *Error) Unwrap() (err error) {
	//: guard against nil receiver so errors.Is on a nil *Error is safe.
	if e == nil {
		//: no receiver — the chain ends here.
		return nil
	}
	//: delegate to Source to keep a single access path.
	return e.Source()
}

// HTTPStatus returns the per-error override if set, otherwise 500. Code is
// never used as an HTTP status directly — callers publishing an HTTP
// response should put the raw code in a header and use this value on the
// status line.
//
// Returns:
//   - int: the HTTP status this Error maps to.
func (e *Error) HTTPStatus() (status int) {
	//: zero override signals "use the default"; non-zero wins.
	if e.httpOverride != 0 {
		//: honour the emitter's per-error override.
		return e.httpOverride
	}
	//: fall back to the safe default.
	return defaultHTTPStatus
}

// ExitCode returns the per-error override if set, otherwise 70 EX_SOFTWARE.
//
// Returns:
//   - int: the POSIX exit code this Error maps to.
func (e *Error) ExitCode() (code int) {
	//: same zero-vs-non-zero discrimination as HTTPStatus.
	if e.exitOverride != 0 {
		//: honour the emitter's per-error override.
		return e.exitOverride
	}
	//: fall back to the safe default.
	return defaultExitCode
}
