// Package errs — defines the SDK-wide typed Error plus the Define
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

// errorBuilderInitialCap is the initial allocation Grow hint for the
// strings.Builder used by (*Error).Error when a trail is present. Matches
// the average rendered length of "[code REASON] public" for the SDK's
// existing sentinel surface.
const errorBuilderInitialCap int = 64

// errorBuilderPerTrailEntry is the per-trail-entry allocation hint added
// on top of errorBuilderInitialCap. Each trail entry adds " <- M.L.P.S"
// (~12 bytes for a typical dotted-quad code).
const errorBuilderPerTrailEntry int = 12

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
func WithHTTPStatus(status int) DefineOption {
	//: return a closure so the option can be passed positionally to Define.
	return func(e *Error) {
		//: store as override so the zero-value still signals "use default".
		e.httpOverride = status
	}
}

// WithExitCode overrides the default POSIX exit code (70) for an Error.
func WithExitCode(code int) DefineOption {
	//: closure mirrors WithHTTPStatus for symmetry at call sites.
	return func(e *Error) {
		//: store as override so zero keeps meaning "default".
		e.exitOverride = code
	}
}

// NewError is a tooling-friendly alias for Define. Prefer Define at call
// sites for consistency; NewError exists so code generators and lints
// expecting a NewXxx constructor on exported types find one.
func NewError(code Code, reason, public, private string, opts ...DefineOption) *Error {
	//: single source of truth lives in Define; NewError is a thin alias.
	return Define(code, reason, public, private, opts...)
}

// Define registers a sentinel-style *Error at package init. It panics
// (with a message citing one of the documentary meta-codes 0.0.0.1..6)
// when validateDefineArgs returns non-nil, so structural mistakes surface
// at program start rather than at the first emission.
func Define(code Code, reason, public, private string, opts ...DefineOption) *Error {
	//: validate up-front so bad sentinels never reach runtime call sites.
	if err := validateDefineArgs(code, reason, public, private); err != nil {
		//: panic at init fails the binary — operators grep the meta-code.
		panic(err.Error())
	}
	//: construct the base sentinel; overrides apply after.
	sentinel := &Error{code: code, reason: reason, public: public, private: private}
	//: apply every Define-time option left-to-right.
	for _, opt := range opts {
		//: each option mutates the fresh instance before it is returned.
		opt(sentinel)
	}
	//: hand back the sentinel ready to be returned from APIs.
	return sentinel
}

// newValidationError is the BOOTSTRAP constructor used by validateDefineArgs
// to surface structural failures WITHOUT re-entering Define (which would
// recurse into validateDefineArgs again and blow the stack at init).
//
// Direct struct literal — does not call Define or validateDefineArgs.
func newValidationError(code Code, reason, public string) *Error {
	//: bypass the whole validation/construction pipeline — this is the
	//: ONLY correct way to surface structural failures from inside the
	//: validator itself without risking init recursion.
	return &Error{code: code, reason: reason, public: public}
}

// Wrap attaches a cause to a new *Error with origin-wins semantics.
// Behaviour contract (see ADR 0005 §3.6):
// 1. cause == nil (interface nil) → stdlib path, source = nil
// 2. cause == (*Error)(nil) typed-nil → stdlib path, source = nil
// 3. cause == (*T)(nil) typed-nil of non-*Error → stdlib path, source preserved
// 4. cause is *Error (directly or behind a stdlib wrapper) → origin-wins,
// params.Code is APPENDED TO THE TRAIL (trail entry 0 is the origin)
// 5. cause is other stdlib error → params.Code is origin, trail stays empty
//
// Runtime policy (v5 HIGH fix): bad WrapParams at runtime does NOT panic;
// the returned *Error carries CodeInvalidWrapParams and preserves cause.
func Wrap(cause error, params WrapParams, fields ...FieldValue) *Error {
	//: case 1 — interface nil: fall straight through to stdlib path.
	if cause == nil {
		//: nil cause means there is no embedded error to inherit from.
		return newFromStdlibCause(nil, params, fields)
	}
	//: case 2 — typed-nil *Error: accessing fields would crash, so we
	//: explicitly catch it here before errors.As ever sees it.
	if typed, ok := cause.(*Error); ok && typed == nil {
		//: typed-nil collapses to the stdlib path with a nil source.
		return newFromStdlibCause(nil, params, fields)
	}
	//: case 4 — walk the chain via errors.AsType to find an *Error even
	//: when it sits behind a fmt.Errorf("%w", …) wrapper.
	if inner, ok := errors.AsType[*Error](cause); ok {
		//: origin-wins path — inherit Code/Reason/Public/Private from inner.
		return wrapSDKCause(cause, inner, params, fields)
	}
	//: cases 3 & 5 — the cause is a plain stdlib (or unknown-typed-nil)
	//: error. params.Code becomes origin; trail stays empty.
	return newFromStdlibCause(cause, params, fields)
}

// wrapSDKCause builds the *Error returned when Wrap finds an *Error in
// the cause chain. Extracted from Wrap so each path through Wrap stays
// readable as a top-to-bottom case analysis instead of one nested branch.
func wrapSDKCause(cause error, inner *Error, params WrapParams, fields []FieldValue) *Error {
	//: trail gains a new wrap-site entry; origin and metadata inherit
	//: from the inner *Error per ADR 0002 §Origin-wins.
	newTrail, trunc := appendTrail(inner, params.Code)
	//: assemble the inheriting *Error in one literal so the helper has a
	//: single allocation point that mirrors newFromStdlibCause downstream.
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

// newFromStdlibCause builds an *Error for causes that are NOT *Error.
// Runtime-safe: validation failure on caller-supplied WrapParams returns
// a typed *Error{CodeInvalidWrapParams} instead of panicking (v5 HIGH fix).
func newFromStdlibCause(cause error, params WrapParams, fields []FieldValue) *Error {
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
		code:         params.Code,
		reason:       params.Reason,
		public:       params.Public,
		private:      params.Private,
		fields:       copied,
		exitOverride: params.ExitCode,
		source:       cause,
	}
}

// Code returns this Error's typed dotted-quad identifier.
func (e *Error) Code() Code {
	//: direct read of the immutable member.
	return e.code
}

// Reason returns this Error's stable SCREAMING_SNAKE identifier.
func (e *Error) Reason() string {
	//: direct read — Reason is part of the API contract.
	return e.reason
}

// Public returns this Error's wire-safe message.
func (e *Error) Public() string {
	//: direct read — Public is wire-safe by construction.
	return e.public
}

// Private returns this Error's log-only detailed message. Never expose
// the return value over any user-facing surface.
func (e *Error) Private() string {
	//: direct read — callers accept the diagnostic-only contract.
	return e.private
}

// Fields returns a defensive copy of the structured metadata attached to
// this Error. Mutating the returned slice has no effect on the Error.
func (e *Error) Fields() []FieldValue {
	//: copy on read so callers cannot mutate our internal state.
	return slices.Clone(e.fields)
}

// Trail returns a defensive copy of the wrap-site chain (newest last).
// The origin's code is NOT in the trail — it lives in Code().
func (e *Error) Trail() []Code {
	//: copy on read — trail entries represent wrap sites, immutable contract.
	return slices.Clone(e.trail)
}

// TrailTruncated reports whether the trail's middle links have been
// dropped due to exceeding maxTrailLen. The flag is MONOTONIC: once set
// by appendTrail, it propagates unchanged to every subsequent wrap.
func (e *Error) TrailTruncated() bool {
	//: direct read — flag is an immutable boolean after construction.
	return e.trailTruncated
}

// Is implements the errors.Is protocol. Three matching modes:
//
// 1. target is *PrefixMatcher → match if origin code OR any trail entry
// satisfies the CIDR-style mask (ADR 0005 prefix routing).
// 2. target is *Error with non-zero Code → match when the two share the
// same (Code, Reason). This lets errors.Is(err, sentinel) succeed
// even when err is a freshly-constructed Wrap result — the typed
// sentinel and the wire sentinel have identical Code+Reason by
// construction, and semantic equivalence is what callers expect.
// The Reason check defuses a hypothetical Code-collision (the
// registry audit is the primary guard; this is belt-and-suspenders).
// 3. anything else → pointer equality (stdlib default).
func (e *Error) Is(target error) bool {
	//: prefix matching path — scan origin code + every trail entry.
	if pm, ok := target.(*PrefixMatcher); ok {
		//: dispatch to the prefix helper so this method stays simple.
		return e.matchesPrefix(pm)
	}
	//: sentinel-by-Code path — two *Error instances with matching Code
	//: and Reason are semantically the same error, even at different
	//: pointer identities (fresh Wrap result vs package-level Define).
	if te, ok := target.(*Error); ok {
		//: delegate so Is() itself stays well under the cyclo budget.
		return e.matchesSentinel(te, target)
	}
	//: default path — the stdlib convention is pointer equality for any
	//: other target type; this short-circuits the outer errors.Is helper safely.
	return e == target
}

// matchesPrefix reports whether this Error's origin code or any trail
// entry satisfies the *PrefixMatcher's (prefix, mask) pair. Extracted from
// Is so the three target-type branches there each remain a single line.
func (e *Error) matchesPrefix(pm *PrefixMatcher) bool {
	//: snapshot prefix+mask once so the comparison loop is cache-friendly.
	prefix := pm.Prefix() & pm.Mask()
	mask := pm.Mask()
	//: origin code is the most common match target — check it first.
	if e.code&mask == prefix {
		//: short-circuit — we matched the origin.
		return true
	}
	//: walk the wrap-site trail; any entry counts as a match.
	for _, trailCode := range e.trail {
		//: per-entry compare against the precomputed prefix.
		if trailCode&mask == prefix {
			//: short-circuit on the first trail hit.
			return true
		}
	}
	//: exhausted origin + trail with no match.
	return false
}

// matchesSentinel reports whether this Error and target (already typed
// as *Error) refer to the same sentinel by (Code, Reason) — and falls
// back to pointer equality if the codes do not match.
func (e *Error) matchesSentinel(te *Error, target error) bool {
	//: zero code defuses the "both uninitialised" edge case.
	if te.code != 0 && e.code == te.code && e.reason == te.reason {
		//: same sentinel identity — Code+Reason guarantee equivalence.
		return true
	}
	//: Code match failed — fall through to pointer equality so callers
	//: deliberately comparing pointers still get stdlib semantics.
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
func (e *Error) Error() string {
	//: fast path — no trail, cheapest formatting.
	if len(e.trail) == 0 {
		//: single concat keeps the no-trail path zero-builder.
		return "[" + e.code.String() + " " + e.reason + "] " + e.public
	}
	//: trail present — assemble via strings.Builder to avoid n^2 concat.
	var builder strings.Builder
	builder.Grow(errorBuilderInitialCap + errorBuilderPerTrailEntry*len(e.trail))
	builder.WriteByte('[')
	builder.WriteString(e.code.String())
	//: render each wrap-site code preceded by the ASCII separator.
	for _, trailCode := range e.trail {
		//: " <- " keeps the rendered form one-line, ASCII-safe.
		builder.WriteString(" <- ")
		builder.WriteString(trailCode.String())
	}
	//: append the truncation marker before the Reason when middle links dropped.
	if e.trailTruncated {
		//: marker is verbatim — operators grep for "(truncated)".
		builder.WriteString(" (truncated)")
	}
	builder.WriteByte(' ')
	builder.WriteString(e.reason)
	builder.WriteString("] ")
	builder.WriteString(e.public)
	//: hand back the finalised representation.
	return builder.String()
}

// Source returns the wrapped cause attached by Wrap.
func (e *Error) Source() error {
	//: direct read — sentinels created by Define have no cause.
	return e.source
}

// Unwrap delegates to Source so the stdlib errors.Is / errors.As machinery
// can walk the chain. The nil-guard also makes this slightly more than a
// pure field getter, which matters for callers storing errors by value.
func (e *Error) Unwrap() error {
	//: guard against nil receiver so errors.Is on a nil *Error is safe.
	if e == nil {
		//: no receiver — the chain ends here.
		return nil
	}
	//: delegate to Source to keep a single access path.
	return e.Source()
}

// HTTPStatus returns the per-error override if set, otherwise 500.
func (e *Error) HTTPStatus() int {
	//: zero override signals "use the default"; non-zero wins.
	if e.httpOverride != 0 {
		//: honour the emitter's per-error override.
		return e.httpOverride
	}
	//: fall back to the safe default.
	return defaultHTTPStatus
}

// ExitCode returns the per-error override if set, otherwise 70 EX_SOFTWARE.
func (e *Error) ExitCode() int {
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
func HasCode(err error, c Code) bool {
	//: nil-chain short-circuit.
	if err == nil {
		//: no chain to walk — definitive miss.
		return false
	}
	//: probe each leg in priority order — outermost code, single-Unwrap
	//: chain, then multi-Unwrap branches. Short-circuit OR keeps the call
	//: count minimal and avoids the consecutive-guard merge violation.
	return errCodeMatches(err, c) ||
		hasCodeInSingleUnwrap(err, c) ||
		hasCodeInMultiUnwrap(err, c)
}

// errCodeMatches reports whether the outermost *Error layer of err
// carries c either as its origin Code or anywhere in its trail.
func errCodeMatches(err error, c Code) bool {
	//: assert to *Error — return false when no SDK layer is at this level.
	sdkErr, ok := err.(*Error)
	if !ok {
		//: nothing to check — caller continues to the Unwrap legs.
		return false
	}
	//: origin code wins fastest.
	if sdkErr.code == c {
		//: short-circuit on origin hit.
		return true
	}
	//: trail check — slices.Contains is the modernised idiom.
	return slices.Contains(sdkErr.trail, c)
}

// hasCodeInSingleUnwrap recurses through the single-error Unwrap()
// interface to keep HasCode below the cyclo budget.
func hasCodeInSingleUnwrap(err error, c Code) bool {
	//: detect a single-error wrapper.
	unwrapper, ok := err.(interface{ Unwrap() error })
	if !ok {
		//: not a single-Unwrap wrapper — leg yields no result.
		return false
	}
	//: descend one level and recurse into HasCode.
	return HasCode(unwrapper.Unwrap(), c)
}

// hasCodeInMultiUnwrap recurses through the multi-error Unwrap()
// interface (e.g. errors.Join) to keep HasCode below the cyclo budget.
func hasCodeInMultiUnwrap(err error, c Code) bool {
	//: detect a multi-error wrapper.
	multiUnwrapper, ok := err.(interface{ Unwrap() []error })
	if !ok {
		//: not a multi-Unwrap wrapper — leg yields no result.
		return false
	}
	//: iterate every child and recurse; first hit wins.
	for _, child := range multiUnwrapper.Unwrap() {
		//: descend into each branch — HasCode itself handles the leaf case.
		if HasCode(child, c) {
			//: short-circuit on the first matching branch.
			return true
		}
	}
	//: exhausted every branch with no match.
	return false
}
