// Package errs is the public facade for SDK errors — both introspection
// (the Of-family accessors and the matchers, re-exported in facade_gen.go,
// beside HasAnyCode and HasAnyReason in accessors.go) and construction
// ([New], [Wrap] and the application Code range in construct.go, the Field
// helpers re-exported in facade_gen.go).
//
// Consumers receive [error] values from the SDK and query them via the
// Of-family accessors below, and — since ADR 0019 — mint their own typed
// errors in the same model with [New] / [Wrap]. The concrete error type
// stays internal (callers see [error], never *errs.Error), so it cannot be
// forged by struct literal; construction goes through the validated
// constructors, which return a typed validation error on malformed input
// rather than panicking. Dashboards, retries, and structured logs branch on
// Code / Reason / HTTPStatus / ExitCode regardless of who built the error.
//
// # Goals
//
//   - Typed dotted-quad codes. Every SDK error carries a Code uint32
//     packed as MM.LL.PP.SS (Major / Layer / Package / Serial).
//     Composable octets — code.Layer(), code.Package() — let routers
//     branch without parsing.
//   - Public / Private split. PublicOf returns a wire-safe message
//     (≤120 runes, no newline). PrivateOf returns the diagnostic
//     envelope — never surface it to consumers.
//   - Validated construction + introspection. Consumers mint typed errors
//     through [New] / [Wrap] (runtime-validated, never panicking) and query
//     received errors through the Of-accessors; the concrete *errs.Error type
//     stays unexported, so it can be built and inspected but never forged.
//   - HTTP / exit-code mapping. Each error has an HTTPStatusOf
//     (default 500) and ExitCodeOf (default 70 / EX_SOFTWARE) so HTTP
//     handlers and CLI binaries can return an SDK error verbatim.
//   - CIDR-style matching. NewPrefixMatcher(code, mask) + errors.Is
//     route entire code-ranges (one package, one layer, one major)
//     with a single call.
//
// # What's shipped
//
// Seven accessors, four predicates, two code helpers, four mask presets, one
// matcher constructor, and the two methods that read a [Field]. Each function
// forwards to internal/kernel/errs — a function, never a variable a consumer
// could reassign for the whole process — and HasAnyCode and HasAnyReason are
// composed here from HasCode and HasReason. One exception: [ReasonOf] is still
// a variable bound to the kernel accessor, because its forwarder measured
// slower; never assign to it.
//
//	| Symbol                                | Kind       | Returns / role                                            |
//	|---------------------------------------|------------|------------------------------------------------------------|
//	| errs.CodeOf(err)                      | accessor   | (Code, bool) — typed dotted-quad, 0/false if none          |
//	| errs.ReasonOf(err)                    | accessor   | (string, bool) — SCREAMING_SNAKE reason                    |
//	| errs.PublicOf(err)                    | accessor   | string — wire-safe message (≤120 runes)                    |
//	| errs.PrivateOf(err)                   | accessor   | string — DIAGNOSTIC ONLY, never surface                    |
//	| errs.HTTPStatusOf(err)                | accessor   | int — HTTP status, default 500                             |
//	| errs.ExitCodeOf(err)                  | accessor   | int — POSIX exit code, default 70 (EX_SOFTWARE)            |
//	| errs.FieldsOf(err)                    | accessor   | []Field — the chain's fields, oldest first; nil if none    |
//	| field.Key()                           | field read | string — the key the emitter chose, such as "problem"      |
//	| field.StringValue()                   | field read | string — the value as text; "" for the zero Field          |
//	| errs.HasCode(err, c)                  | predicate  | bool — true if c appears in the Unwrap chain               |
//	| errs.HasAnyCode(err, c1, c2, …)       | predicate  | bool — variadic OR over multiple codes (routing on a set)  |
//	| errs.HasReason(err, r)                | predicate  | bool — same with the reason string                         |
//	| errs.HasAnyReason(err, r1, r2, …)     | predicate  | bool — variadic OR over multiple reasons                   |
//	| errs.NewPrefixMatcher(code, mask)     | matcher    | PrefixMatcher — use with errors.Is for range routing       |
//	| errs.Pack(major, layer, pkg, serial)  | code ctor  | Code from four octets                                       |
//	| errs.ParseCode(s)                     | code ctor  | Code from "M.L.P.S" string                                 |
//	| errs.MaskByMajor / Layer / Package    | mask const | feed NewPrefixMatcher for the relevant subnet              |
//	| errs.MaskExact                        | mask const | exact-code match (equivalent to HasCode)                   |
//
// Type aliases: errs.Code (a uint32), errs.Major / Layer / PkgCode /
// Serial (octet types), errs.PrefixMatcher.
//
// # Surface
//
// Accessors walk the Unwrap chain (both `Unwrap() error` and
// `Unwrap() []error`) and return the deepest *errs.Error value
// encountered. Defaults document the behaviour when no SDK error is
// present:
//
//	func CodeOf(err error)       (Code, bool)    // 0 / false if none — typed dotted-quad
//	func ReasonOf(err error)     (string, bool)  // "" / false if none
//	func PublicOf(err error)     string          // "" if none
//	func PrivateOf(err error)    string          // "" if none — DIAGNOSTIC ONLY
//	func HTTPStatusOf(err error) int             // 500 default
//	func ExitCodeOf(err error)   int             // 70 (EX_SOFTWARE) default
//	func FieldsOf(err error)     []Field         // nil if none — oldest cause first
//	func HasCode(err error, code Code) bool
//	func HasReason(err error, reason string) bool
//
// Octets are reached on the typed [Code] itself: code.Major(),
// code.Layer(), code.Package(), code.Serial(). No separate LayerOf
// accessor exists — the kernel exports exactly one accessor per field
// and this package forwards it unchanged.
//
// # Quick start
//
//	import (
//	    "github.com/kitsunium/sdk/pkg/v1/errs"
//	    "github.com/kitsunium/sdk/pkg/v1/observe/logger"
//	)
//
//	_, err := logger.NewText(logger.Config{})  // nil Writer → fails
//	// 0x01_01_00_01 = 1.1.0.1 (pkg/v1/observe/logger WriterRequired under ADR 0005).
//	if errs.HasCode(err, 0x01_01_00_01) {
//	    // configuration problem on our side
//	}
//	fmt.Println("wire-safe message:", errs.PublicOf(err))
//	if code, ok := errs.CodeOf(err); ok {
//	    fmt.Println("layer:", code.Layer())
//	}
//	fmt.Println("HTTP status:", errs.HTTPStatusOf(err))
//
// # The Public/Private split
//
// [PublicOf] returns the wire-safe message (≤120 runes, literal, no
// interpolation). Send it in HTTP/gRPC responses, error pages, and
// user-facing surfaces.
//
// [PrivateOf] returns the detailed log-only message. DIAGNOSTIC ONLY.
// Never put it in a response, an error page, or anything the end user
// can see. It exists so observability tooling can correlate a request
// ID with a detailed server-side explanation in the log backend
// without re-logging the entire chain.
//
// # Reading the fields an error carries
//
// [FieldsOf] returns the structured clauses the emitters along the chain
// attached, oldest cause first and newest wrapper last, as a copy the caller
// owns. Each is read with Key and StringValue — the value as text: a string
// verbatim, a number in decimal, a bool as true or false. A key may appear at
// more than one depth of a chain, which is why no map-shaped accessor exists:
// the caller decides which depth it wants.
//
//	_, err := mail.ParseURL(raw)
//	for _, field := range errs.FieldsOf(err) {
//	    if field.Key() == "problem" {
//	        fmt.Println("SMTP URL", field.StringValue()) // "has a port that is not a number from 1 to 65535"
//	    }
//	}
//
// A field value is a clause an emitter CHOSE to attach — a name, a position, a
// reason — and no SDK emitter attaches a value it was given to protect:
// mail.ParseURL says which part of a URL is wrong and never quotes the URL,
// whose userinfo is the password; the secret domain names a secret and never
// its value. That rule is what makes the fields safe to read, and a consumer
// minting its own errors with [New] and [Wrap] owes its readers the same. The
// one field whose text the SDK does not write is "cause", which carries a
// foreign error's message as its library wrote it.
//
// Safe to read is not safe to publish: fields are diagnostics, like
// [PrivateOf] — authz attaches the subject, the action and the resource — so
// they belong in a log line or an operator's report, never in an HTTP response
// or an error page.
//
// # HTTP status policy
//
// [HTTPStatusOf] defaults to 500 when the error does not carry an
// explicit override. That default is deliberate for internal failures
// but it is a leak-by-default anti-pattern for domain errors that are
// really 4xx (validation, not-found, conflict, authorisation). Such
// errors MUST pass errs.WithHTTPStatus(4xx) at Define time so the
// accessor surfaces the correct status.
//
// # Semantics reminders
//
//   - Origin wins on wrap. An error born in service/observe/logger (code 31xx) and
//     observed through pkg/v1/observe/logger keeps its 31xx code — the Code
//     describes the origin, never the observation surface.
//   - errors.Is keeps stdlib semantics. For code / reason matching use
//     the explicit [HasCode] / [HasReason] helpers; errors.Is(err,
//     context.Canceled) still walks the chain through errs.Wrap.
//   - Default HTTP / exit codes are global (500 / 70). Per-error
//     overrides come from errs.WithHTTPStatus / WithExitCode inside the
//     emitter package.
//
// # PrefixMatcher routing
//
// [NewPrefixMatcher] returns a sentinel that classifies any wrapped
// error by Code prefix when used through errors.Is. Combine with
// [MaskByMajor] / [MaskByLayer] / [MaskByPackage] / [MaskExact] for
// CIDR-style code routing in dashboards / middleware. Single-code
// matching uses [HasCode] which is cheaper.
//
// Package errs — construction half of the public error API.
//
// facade_gen.go re-exports the read-only introspection surface; construct.go
// and the Field helpers re-exported beside it are the *construction*
// surface, so that an external consumer (any module adopting this SDK, with
// no internal/ access) can mint typed SDK errors rather than only
// inspecting SDK-origin ones. Together they let a downstream
// codebase migrate off fmt.Errorf / errors.New onto the SDK error model
// wholesale — every error carrying a dotted-quad Code, a wire-safe Public
// message, and a log-only Private envelope.
//
// # Construction vs. Define
//
// SDK-internal packages mint sentinels with internal/kernel/errs.Define, which
// panics at init on a malformed sentinel — safe because a build-time AST audit
// proves every Define call well-formed before the binary ships. External
// consumers get no such audit, so [New] and [Wrap] follow a runtime policy
// instead: a structural failure (bad code, non-SCREAMING_SNAKE reason,
// empty/over-long/multiline public, empty private) returns a typed validation
// error (CodeInvalidCode / Reason / Public / Private) — the result is always a
// usable, introspectable SDK error, never nil and never a panic.
//
// # Code space for third-party modules
//
// The dotted-quad MM.LL.PP.SS taxonomy (ADR 0005) assumes a coordinated Major
// (MM) octet. The SDK only ever allocates Major in the range [0, MinAppMajor)
// — 0 for internal codes, then the public semver major (1 for pkg/v1, 2 for a
// future pkg/v2, …). To stay collision-free with the SDK now and across every
// future SDK release, a consumer assigns its own codes a Major in
// [MinAppMajor, MaxMajor] (0x40–0x7F). Declare them as hex literals exactly as
// the SDK does internally:
//
//	// myapp/errcodes.go — Major 0x40 is application-owned, never SDK-issued.
//	const (
//	    CodeUserNotFound errs.Code = 0x40_01_01_01 // 64.1.1.1
//	    CodeOrderExpired errs.Code = 0x40_01_02_01 // 64.1.2.1
//	)
//
//	var ErrUserNotFound = errs.New(CodeUserNotFound, "USER_NOT_FOUND",
//	    "user not found", "lookup miss in users table")
//
// The Major ceiling is 0x7F because every Code must round-trip through a
// positive int32 (the kernel keeps the top uint32 bit clear); 64 application
// majors is far more than the SDK's semver line will ever consume.
package errs
