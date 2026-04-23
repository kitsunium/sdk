// Package errs: error.go defines the SDK-wide typed Error plus the Define
// and Wrap constructors. Consumers never import this package directly —
// they introspect errors through github.com/kitsunium/sdk/pkg/v1/errs.
package errs

import (
	"errors"
	"slices"
	"strings"
)

// defaultHTTPStatus is the wire status returned when an Error does not
// override it. 500 is deliberately generic — callers who want layer-aware
// mapping put the raw Code in a response header.
const defaultHTTPStatus int = 500

// defaultExitCode is the POSIX exit status returned when an Error does not
// override it. 70 matches sysexits EX_SOFTWARE (internal software error).
const defaultExitCode int = 70

// Error is the SDK-wide typed error. Fields are unexported — consumers
// obtain values via the getter methods on *Error or via the Of-accessors
// in github.com/kitsunium/sdk/pkg/v1/errs. Instances are immutable after
// construction: Wrap returns a new *Error, never mutates the input.
type Error struct {
	code           Code
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
//   - code: typed Code identifier (dotted-quad; see ADR 0005).
//   - reason: SCREAMING_SNAKE stable identifier (matches the enclosing var).
//   - public: wire-safe message; the AST audit requires a string literal.
//   - private: log-only detailed message; may reference internal concepts.
//   - opts: optional Define-time overrides (WithHTTPStatus, WithExitCode).
//
// Returns:
//   - *Error: delegation to Define, identical semantics.
func NewError(code Code, reason, public, private string, opts ...DefineOption) (e *Error) {
	//: single source of truth lives in Define; NewError is a thin alias.
	return Define(code, reason, public, private, opts...)
}

// NewErrorInt is a DEPRECATED shim for transition from pre-ADR-0005 int codes.
//
// Deprecated: use NewError with a typed Code constant. Removed before v1.0.0.
func NewErrorInt(code int, reason, public, private string, opts ...DefineOption) (e *Error) {
	return Define(Code(uint32(code)), reason, public, private, opts...)
}

// Define registers a sentinel-style *Error at package init. It panics
// (with a message citing one of the documentary meta-codes 0.0.0.1..6)
// when validateDefineArgs returns non-nil, so structural mistakes surface
// at program start rather than at the first emission.
//
// Params:
//   - code: typed Code identifier (dotted-quad; see ADR 0005).
//   - reason: SCREAMING_SNAKE stable identifier (matches the enclosing var).
//   - public: wire-safe message; the AST audit requires a string literal.
//   - private: log-only detailed message; may reference internal concepts.
//   - opts: optional Define-time overrides (WithHTTPStatus, WithExitCode).
//
// Returns:
//   - *Error: a fully constructed sentinel ready to be returned from APIs.
func Define(code Code, reason, public, private string, opts ...DefineOption) (e *Error) {
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

// DefineInt is a DEPRECATED shim for transition from pre-ADR-0005 int codes.
//
// Deprecated: use Define with a typed Code constant. Removed before v1.0.0.
func DefineInt(code int, reason, public, private string, opts ...DefineOption) (e *Error) {
	return Define(Code(uint32(code)), reason, public, private, opts...)
}

// newValidationError is the BOOTSTRAP constructor used by validateDefineArgs
// to surface structural failures WITHOUT re-entering Define (which would
// recurse into validateDefineArgs again and blow the stack at init).
//
// Direct struct literal — does not call Define or validateDefineArgs.
//
// Params:
//   - code: the meta-code identifying the structural failure.
//   - reason: SCREAMING_SNAKE reason matching the meta-code.
//   - public: human-readable failure description.
//
// Returns:
//   - *Error: a bootstrap-only Error with no Private and no fields.
func newValidationError(code Code, reason, public string) (e *Error) {
	//: bypass the whole validation/construction pipeline — this is the
	//: ONLY correct way to surface structural failures from inside the
	//: validator itself without risking init recursion.
	return &Error{code: code, reason: reason, public: public}
}

// Wrap attaches a cause to a new *Error with origin-wins semantics.
// Behaviour contract (see ADR 0005 §3.6):
//  1. cause == nil (interface nil) → stdlib path, source = nil
//  2. cause == (*Error)(nil) typed-nil → stdlib path, source = nil
//  3. cause == (*T)(nil) typed-nil of non-*Error → stdlib path, source preserved
//  4. cause is *Error (directly or behind a stdlib wrapper) → origin-wins,
//     params.Code is APPENDED TO THE TRAIL (trail entry 0 is the origin)
//  5. cause is other stdlib error → params.Code is origin, trail stays empty
//
// Runtime policy (v5 HIGH fix): bad WrapParams at runtime does NOT panic;
// the returned *Error carries CodeInvalidWrapParams and preserves cause.
//
// Params:
//   - cause: original error being wrapped; may be nil or typed-nil.
//   - params: the code / reason / public / private used only on the stdlib path.
//   - fields: metadata always appended (both paths).
//
// Returns:
//   - *Error: a fresh *Error wrapping the cause; never mutates input.
func Wrap(cause error, params WrapParams, fields ...FieldValue) (e *Error) {
	//: case 1 — interface nil: fall straight through to stdlib path.
	if cause == nil {
		return newFromStdlibCause(nil, params, fields)
	}
	//: case 2 — typed-nil *Error: accessing fields would crash, so we
	//: explicitly catch it here before errors.As ever sees it.
	if typed, ok := cause.(*Error); ok && typed == nil {
		return newFromStdlibCause(nil, params, fields)
	}
	//: case 4 — walk the chain via stdlib errors.As to find an *Error even
	//: when it sits behind a fmt.Errorf("%w", …) wrapper.
	var inner *Error
	if errors.As(cause, &inner) {
		//: trail gains a new wrap-site entry; origin and metadata inherit
		//: from the inner *Error per ADR 0002 §Origin-wins.
		newTrail, trunc := appendTrail(inner, params.Code)
		return &Error{
			code:           inner.code,
			reason:         inner.reason,
			public:         inner.public,
			private:        inner.private,
			fields:         slices.Concat(inner.fields, fields),
			trail:          newTrail,
			trailTruncated: trunc,
			httpOverride:   inner.httpOverride,
			exitOverride:   inner.exitOverride,
			source:         cause,
		}
	}
	//: cases 3 & 5 — the cause is a plain stdlib (or unknown-typed-nil)
	//: error. params.Code becomes origin; trail stays empty.
	return newFromStdlibCause(cause, params, fields)
}

// newFromStdlibCause builds an *Error for causes that are NOT *Error.
// Runtime-safe: validation failure on caller-supplied WrapParams returns
// a typed *Error{CodeInvalidWrapParams} instead of panicking (v5 HIGH fix).
//
// Params:
//   - cause: the underlying stdlib error (may be nil or typed-nil).
//   - params: caller-supplied code/reason/public/private.
//   - fields: metadata attached to the returned *Error.
//
// Returns:
//   - *Error: a freshly constructed Error; source preserves cause.
func newFromStdlibCause(cause error, params WrapParams, fields []FieldValue) (e *Error) {
	//: runtime policy — bad params at wrap time MUST NOT crash the goroutine.
	if bad := validateDefineArgs(params.Code, params.Reason, params.Public, params.Private); bad != nil {
		//: typed fallback keeps observability: callers see CodeInvalidWrapParams
		//: and the private field records the underlying validation detail.
		return &Error{
			code:    CodeInvalidWrapParams,
			reason:  "INVALID_WRAP_PARAMS",
			public:  "internal wrap failure",
			private: "Wrap received invalid params: " + bad.Public(),
			source:  cause,
		}
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

// Code returns this Error's numeric identifier as an int.
//
// Deprecated: use CodeValue() Code. Kept at v1 for backward compatibility;
// removed at v2. The int return is safe on 64-bit GOARCH (enforced by the
// build tag in code.go) because Code is uint32 and int is 8 bytes.
//
// Returns:
//   - int: the dotted-quad Code as a plain int.
func (e *Error) Code() (code int) {
	//: cast through uint32 for portability — Code is uint32-backed.
	return int(uint32(e.code))
}

// CodeValue returns the typed dotted-quad Code.
//
// Returns:
//   - Code: the Code assigned at Define time.
func (e *Error) CodeValue() (c Code) {
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

// Trail returns a defensive copy of the wrap-site chain (newest last).
// The origin's code is NOT in the trail — it lives in CodeValue().
//
// Returns:
//   - []Code: a fresh slice; mutating it does not affect the Error.
func (e *Error) Trail() (out []Code) {
	//: copy on read — trail entries represent wrap sites, immutable contract.
	return slices.Clone(e.trail)
}

// TrailTruncated reports whether the trail's middle links have been
// dropped due to exceeding maxTrailLen. The flag is MONOTONIC: once set
// by appendTrail, it propagates unchanged to every subsequent wrap.
//
// Returns:
//   - bool: true iff at least one middle link was dropped.
func (e *Error) TrailTruncated() (truncated bool) {
	//: direct read — flag is an immutable boolean after construction.
	return e.trailTruncated
}

// Layer returns the layer octet of this Error's Code as an int.
//
// Deprecated: use CodeValue().Layer(). Kept at v1 for backward compat.
//
// Returns:
//   - int: the layer byte extracted from Code.
func (e *Error) Layer() (layer int) {
	//: delegate to Code.Layer() and widen for v1 compat.
	return int(e.code.Layer())
}

// Is implements the errors.Is protocol. Three matching modes:
//
//  1. target is *PrefixMatcher → match if origin code OR any trail entry
//     satisfies the CIDR-style mask (ADR 0005 prefix routing).
//  2. target is *Error with non-zero Code → match when the two share the
//     same (Code, Reason). This lets errors.Is(err, sentinel) succeed
//     even when err is a freshly-constructed Wrap result — the typed
//     sentinel and the wire sentinel have identical Code+Reason by
//     construction, and semantic equivalence is what callers expect.
//     The Reason check defuses a hypothetical Code-collision (the
//     registry audit is the primary guard; this is belt-and-suspenders).
//  3. anything else → pointer equality (stdlib default).
//
// Params:
//   - target: the error being compared against.
//
// Returns:
//   - bool: true iff the comparison succeeds per the rules above.
func (e *Error) Is(target error) (ok bool) {
	//: prefix matching path — scan origin code + every trail entry.
	if pm, pok := target.(*PrefixMatcher); pok {
		if e.code&pm.Mask() == pm.Prefix()&pm.Mask() {
			return true
		}
		for _, c := range e.trail {
			if c&pm.Mask() == pm.Prefix()&pm.Mask() {
				return true
			}
		}
		return false
	}
	//: sentinel-by-Code path — two *Error instances with matching Code
	//: and Reason are semantically the same error, even at different
	//: pointer identities (fresh Wrap result vs package-level Define).
	if te, tok := target.(*Error); tok {
		//: zero code defuses the "both uninitialised" edge case.
		if te.code != 0 && e.code == te.code && e.reason == te.reason {
			return true
		}
		//: Code match failed — fall through to pointer equality so
		//: callers deliberately comparing pointers still get stdlib
		//: semantics. Rare but preserves backward compat.
		return e == target
	}
	//: default path — the stdlib convention is pointer equality for any
	//: other target type; this short-circuits the outer errors.Is helper safely.
	return e == target
}

// Error returns the neutral stable form "[<code> <REASON>] <public>". It
// never includes Private content nor Fields, so callers may safely bubble
// err.Error() across any boundary without leaking diagnostic data.
//
// When the trail is non-empty, wrap-site codes are rendered between the
// origin code and the Reason with ASCII "<-" separators, e.g.:
//
//	[0.3.2.1 <- 1.2.0.3 JSON_MARSHAL_FAILED] invalid input
//
// Truncated trails append " (truncated)" before the Reason marker.
//
// Returns:
//   - string: the stable neutral representation.
func (e *Error) Error() (s string) {
	//: fast path — no trail, cheapest formatting.
	if len(e.trail) == 0 {
		return "[" + e.code.String() + " " + e.reason + "] " + e.public
	}
	//: trail present — assemble via strings.Builder to avoid n^2 concat.
	var b strings.Builder
	b.Grow(64 + 12*len(e.trail))
	b.WriteByte('[')
	b.WriteString(e.code.String())
	for _, c := range e.trail {
		b.WriteString(" <- ")
		b.WriteString(c.String())
	}
	if e.trailTruncated {
		b.WriteString(" (truncated)")
	}
	b.WriteByte(' ')
	b.WriteString(e.reason)
	b.WriteString("] ")
	b.WriteString(e.public)
	return b.String()
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

// HTTPStatus returns the per-error override if set, otherwise 500.
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

// HasCode returns true if any *Error in err's chain has code == c OR any
// trail entry matches c. The walk recurses through BOTH single-error
// wrappers (Unwrap() error) AND multi-error wrappers (Unwrap() []error,
// e.g. errors.Join). Time complexity: O(total_errors + total_trail_lengths).
//
// Params:
//   - err: the error chain to inspect.
//   - c: the Code to search for.
//
// Returns:
//   - bool: true iff c is found anywhere in the chain (code or trail).
func HasCode(err error, c Code) (found bool) {
	//: nil-chain short-circuit.
	if err == nil {
		return false
	}
	//: direct identity check on the outermost *Error (if any).
	if e, ok := err.(*Error); ok {
		if e.code == c {
			return true
		}
		for _, t := range e.trail {
			if t == c {
				return true
			}
		}
	}
	//: recurse via single-error Unwrap (fmt.Errorf, manual wrappers).
	if u, ok := err.(interface{ Unwrap() error }); ok {
		if HasCode(u.Unwrap(), c) {
			return true
		}
	}
	//: recurse via multi-error Unwrap (errors.Join).
	if u, ok := err.(interface{ Unwrap() []error }); ok {
		for _, child := range u.Unwrap() {
			if HasCode(child, c) {
				return true
			}
		}
	}
	return false
}
