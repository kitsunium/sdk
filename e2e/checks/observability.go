// Package checks holds the per-domain conformance suites for the SDK e2e binary.
package checks

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/kitsunium/sdk/e2e/harness"
	"github.com/kitsunium/sdk/pkg/v1/codec"
	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/logger"
)

// loggerDomain labels every logger-suite Result.
const loggerDomain = "logger"

// errsDomain labels every errs-suite Result.
const errsDomain = "errs"

// frameworkVersionKey is the auto-injected attribute every emitted record carries.
const frameworkVersionKey = "framework_version"

// unknownFormat is a Format string no service codec registers, so Unmarshal
// returns the typed UNKNOWN_FORMAT sentinel deterministically on every OS.
const unknownFormat codec.Format = "no-such-format-xyz"

// unknownReason is the SCREAMING_SNAKE reason the unknownFormat error carries.
const unknownReason = "UNKNOWN_FORMAT"

// foreignReason is a reason from another package, used to prove HasReason rejects it.
const foreignReason = "WRITER_REQUIRED"

// httpDefaultStatus is the documented HTTPStatusOf fallback for a non-SDK error.
const httpDefaultStatus int = 500

// exitDefaultCode is the documented ExitCodeOf fallback (EX_SOFTWARE).
const exitDefaultCode int = 70

// httpStatusFloor is the lowest sane HTTP status code (informational 1xx).
const httpStatusFloor int = 100

// httpStatusCeil is the highest sane HTTP status code (server 5xx).
const httpStatusCeil int = 599

// addrAttrKey / addrAttrVal are a String attribute exercised by the attr check.
const addrAttrKey = "addr"

// addrAttrVal is the value paired with addrAttrKey.
const addrAttrVal = ":8080"

// ageAttrKey is an Int attribute key exercised by the attr and builder checks.
const ageAttrKey = "age"

// ageAttrVal is the value paired with ageAttrKey.
const ageAttrVal int = 36

// userAttrKey / userAttrVal are the builder check's String attribute pair.
const userAttrKey = "user"

// userAttrVal is the value paired with userAttrKey.
const userAttrVal = "ada"

// readyMsg is the message asserted to reach the in-memory buffer.
const readyMsg = "service ready"

// builderMsg is the message the chainable builder emits.
const builderMsg = "user logged in"

// debugMsg is emitted at Debug and must be suppressed under MinLevel=Info.
const debugMsg = "debug-suppressed"

// infoMsg is emitted at Info and must survive the MinLevel=Info filter.
const infoMsg = "info-delivered"

// Octet expectations for the UNKNOWN_FORMAT code (dotted-quad 1.2.0.1).
const (
	// wantMajor is the MM octet — SemVer major (1 = pkg/v1 surface).
	wantMajor int = 1
	// wantLayer is the LL octet — SDK layer (2 = core/codec block).
	wantLayer int = 2
	// wantPkg is the PP octet — per-layer package slot (0 = codec facade).
	wantPkg int = 0
	// wantSerial is the SS octet — per-package serial (1 = UNKNOWN_FORMAT).
	wantSerial int = 1
)

// errPlainNonSDK is a stdlib error carrying no *errs.Error, used to prove the
// introspection accessors degrade to their documented defaults.
var errPlainNonSDK = errors.New("plain non-sdk error")

// Logger returns the logger-domain conformance checks (Default/NewText, levels,
// attributes, sinks, framework version).
func Logger() harness.CheckGroup {
	//: each check exercises one observable logger behaviour on the real host.
	return harness.CheckGroup{
		Domain: loggerDomain,
		Checks: []harness.Check{
			checkLoggerTextToBuffer,
			checkLoggerAttributes,
			checkLoggerFrameworkVersion,
			checkLoggerLevelFilter,
			checkLoggerBuilder,
			checkLoggerDefault,
		},
	}
}

// Errs returns the errs-domain conformance checks (CodeOf/ReasonOf/PublicOf/
// HasCode introspection over real SDK errors).
func Errs() harness.CheckGroup {
	//: each check exercises one read-only introspection accessor over a real
	//: SDK error produced by the pure-Go codec/logger facades.
	return harness.CheckGroup{
		Domain: errsDomain,
		Checks: []harness.Check{
			checkErrsCode,
			checkErrsReason,
			checkErrsPublicPrivate,
			checkErrsHasCode,
			checkErrsHasReason,
			checkErrsCodeOctets,
			checkErrsExitAndHTTP,
			checkErrsNilDegrades,
		},
	}
}

// newBufferLogger wires a text Logger writing into a fresh bytes.Buffer at the
// given minimum level, returning both so a check can emit then inspect bytes.
func newBufferLogger(min logger.Level) (buf *bytes.Buffer, lg logger.Logger, err error) {
	//: an in-memory buffer lets the check read back exactly what was emitted.
	buf = &bytes.Buffer{}
	//: NewText with a single Writer routes every record into buf.
	lg, err = logger.NewText(logger.Config{Writer: buf, MinLevel: min})
	//: hand the buffer back even on error so the caller can build a detail string.
	return buf, lg, err
}

// checkLoggerTextToBuffer asserts an Info record's message text reaches an
// in-memory buffer through the public NewText path.
func checkLoggerTextToBuffer() harness.Result {
	//: build a logger writing into a buffer at the default Info level.
	buf, lg, err := newBufferLogger(logger.LevelInfo)
	//: a construction failure means the public path is broken on this host.
	if err != nil {
		//: surface the typed construction error as the failure detail.
		return harness.Failed(loggerDomain, "text-to-buffer", fmt.Sprintf("NewText: %v", err))
	}
	//: emit one Info record carrying the message under test.
	logger.Info(context.Background(), lg, readyMsg)
	//: the rendered line must contain the verbatim message text.
	if !strings.Contains(buf.String(), readyMsg) {
		//: a missing message means the encoder/sink wiring did not deliver.
		return harness.Failed(loggerDomain, "text-to-buffer", fmt.Sprintf("buffer lacks message: %q", buf.String()))
	}
	//: the message was rendered into the buffer — the path works.
	return harness.Passed(loggerDomain, "text-to-buffer", fmt.Sprintf("message rendered: %q", strings.TrimSpace(buf.String())))
}

// checkLoggerAttributes asserts typed String/Int attrs render as key=value
// pairs in the emitted record.
func checkLoggerAttributes() harness.Result {
	//: build a buffer-backed logger at Info to capture the attributes.
	buf, lg, err := newBufferLogger(logger.LevelInfo)
	//: a construction failure means the attribute path cannot be exercised.
	if err != nil {
		//: surface the typed construction error as the failure detail.
		return harness.Failed(loggerDomain, "typed-attrs", fmt.Sprintf("NewText: %v", err))
	}
	//: emit a record carrying one String and one Int typed attribute.
	logger.Info(context.Background(), lg, "with attrs", logger.String(addrAttrKey, addrAttrVal), logger.Int(ageAttrKey, ageAttrVal))
	out := buf.String()
	//: the String attr renders quoted as addr=":8080".
	wantStr := fmt.Sprintf("%s=%q", addrAttrKey, addrAttrVal)
	//: the Int attr renders unquoted as age=36.
	wantInt := fmt.Sprintf("%s=%d", ageAttrKey, ageAttrVal)
	//: both typed attributes must appear in the rendered line.
	if !strings.Contains(out, wantStr) || !strings.Contains(out, wantInt) {
		//: a missing attribute means typed rendering regressed on this host.
		return harness.Failed(loggerDomain, "typed-attrs", fmt.Sprintf("want %s and %s in %q", wantStr, wantInt, out))
	}
	//: both typed attributes rendered as expected.
	return harness.Passed(loggerDomain, "typed-attrs", fmt.Sprintf("rendered %s %s", wantStr, wantInt))
}

// checkLoggerFrameworkVersion asserts every record is auto-decorated with the
// framework_version attribute by the public construction path.
func checkLoggerFrameworkVersion() harness.Result {
	//: build a buffer-backed logger to inspect the auto-injected attribute.
	buf, lg, err := newBufferLogger(logger.LevelInfo)
	//: a construction failure means the decoration path cannot be exercised.
	if err != nil {
		//: surface the typed construction error as the failure detail.
		return harness.Failed(loggerDomain, "framework-version", fmt.Sprintf("NewText: %v", err))
	}
	//: emit any record — NewText decorates it with framework_version.
	logger.Info(context.Background(), lg, "versioned")
	out := buf.String()
	//: the auto-injected key must appear with the FrameworkVersion() value.
	want := fmt.Sprintf("%s=%q", frameworkVersionKey, logger.FrameworkVersion())
	//: a missing framework_version means the With-decoration regressed.
	if !strings.Contains(out, want) {
		//: report what was observed so the failure is diagnosable.
		return harness.Failed(loggerDomain, "framework-version", fmt.Sprintf("want %s in %q", want, out))
	}
	//: the framework_version attribute was auto-injected as expected.
	return harness.Passed(loggerDomain, "framework-version", fmt.Sprintf("auto-injected %s", want))
}

// checkLoggerLevelFilter asserts a Debug record is suppressed when the minimum
// level is Info while an Info record at the same logger is delivered.
func checkLoggerLevelFilter() harness.Result {
	//: build a logger filtering at Info so Debug records are dropped at source.
	buf, lg, err := newBufferLogger(logger.LevelInfo)
	//: a construction failure means the filter path cannot be exercised.
	if err != nil {
		//: surface the typed construction error as the failure detail.
		return harness.Failed(loggerDomain, "level-filter", fmt.Sprintf("NewText: %v", err))
	}
	//: emit one Debug (below the floor) and one Info (at the floor) record.
	logger.Debug(context.Background(), lg, debugMsg)
	logger.Info(context.Background(), lg, infoMsg)
	out := buf.String()
	//: the Debug record must be absent and the Info record present.
	if strings.Contains(out, debugMsg) || !strings.Contains(out, infoMsg) {
		//: a leaked Debug or dropped Info means filtering is broken here.
		return harness.Failed(loggerDomain, "level-filter", fmt.Sprintf("filtering wrong: %q", out))
	}
	//: filtering held — Debug suppressed, Info delivered.
	return harness.Passed(loggerDomain, "level-filter", "Debug suppressed, Info delivered at MinLevel=Info")
}

// checkLoggerBuilder asserts the chainable Build(lg, level) hot path delivers a
// record with its typed Str/Int attributes.
func checkLoggerBuilder() harness.Result {
	//: build a buffer-backed logger so the builder's output is observable.
	buf, lg, err := newBufferLogger(logger.LevelInfo)
	//: a construction failure means the builder path cannot be exercised.
	if err != nil {
		//: surface the typed construction error as the failure detail.
		return harness.Failed(loggerDomain, "builder", fmt.Sprintf("NewText: %v", err))
	}
	//: drive the chainable builder; Send terminates the chain and emits.
	logger.Build(lg, logger.LevelInfo).Str(userAttrKey, userAttrVal).Int(ageAttrKey, ageAttrVal).Send(context.Background(), builderMsg)
	out := buf.String()
	//: the String attr renders quoted as user="ada".
	wantStr := fmt.Sprintf("%s=%q", userAttrKey, userAttrVal)
	//: the Int attr renders unquoted as age=36.
	wantInt := fmt.Sprintf("%s=%d", ageAttrKey, ageAttrVal)
	//: the message and both builder-supplied attributes must be rendered.
	if !strings.Contains(out, builderMsg) || !strings.Contains(out, wantStr) || !strings.Contains(out, wantInt) {
		//: report the observed line for diagnosis.
		return harness.Failed(loggerDomain, "builder", fmt.Sprintf("builder output incomplete: %q", out))
	}
	//: the builder hot path emitted the message with both typed attributes.
	return harness.Passed(loggerDomain, "builder", fmt.Sprintf("Build chain emitted %q with %s", builderMsg, wantStr))
}

// checkLoggerDefault asserts logger.Default() honours its no-error contract and
// returns a usable (non-nil) Logger.
func checkLoggerDefault() harness.Result {
	//: Default() supplies os.Stderr explicitly, so it must never error.
	lg, err := logger.Default()
	//: a non-nil error would break the documented stderr one-liner contract.
	if err != nil {
		//: surface the unexpected error as the failure detail.
		return harness.Failed(loggerDomain, "default", fmt.Sprintf("Default returned error: %v", err))
	}
	//: a nil Logger despite a nil error would be an invariant violation.
	if lg == nil {
		//: report the broken contract.
		return harness.Failed(loggerDomain, "default", "Default returned nil Logger with nil error")
	}
	//: Default() honoured its no-error, non-nil-Logger contract.
	return harness.Passed(loggerDomain, "default", "Default() returned a usable Logger, no error")
}

// codecUnknownFormatErr produces a real SDK error deterministically on every OS:
// Unmarshal against an unregistered Format returns the UNKNOWN_FORMAT sentinel.
func codecUnknownFormatErr() error {
	//: target a holder; the codec lookup fails before any decode happens.
	var holder map[string]any
	//: an unregistered Format makes the facade return its typed sentinel.
	return codec.Unmarshal(unknownFormat, []byte(`{}`), &holder)
}

// checkErrsCode asserts CodeOf returns the expected non-zero typed Code for a
// real SDK error.
func checkErrsCode() harness.Result {
	//: produce the pure-Go SDK error under introspection.
	err := codecUnknownFormatErr()
	//: CodeOf must report the typed dotted-quad Code with ok==true.
	code, ok := errs.CodeOf(err)
	//: a missing or mismatched code means the error did not carry the sentinel.
	if !ok || code != codec.CodeUnknownFormat {
		//: report the observed code for diagnosis.
		return harness.Failed(errsDomain, "code-of", fmt.Sprintf("CodeOf=%v ok=%v want %v", code, ok, codec.CodeUnknownFormat))
	}
	//: CodeOf returned the expected non-zero typed Code.
	return harness.Passed(errsDomain, "code-of", fmt.Sprintf("CodeOf=%v (%d.%d.%d.%d)", code, code.Major(), code.Layer(), code.Package(), code.Serial()))
}

// checkErrsReason asserts ReasonOf returns the expected SCREAMING_SNAKE reason.
func checkErrsReason() harness.Result {
	//: produce the pure-Go SDK error under introspection.
	err := codecUnknownFormatErr()
	//: ReasonOf must report the SCREAMING_SNAKE reason with ok==true.
	reason, ok := errs.ReasonOf(err)
	//: a wrong or missing reason means routing-by-reason would break.
	if !ok || reason != unknownReason {
		//: report the observed reason for diagnosis.
		return harness.Failed(errsDomain, "reason-of", fmt.Sprintf("ReasonOf=%q ok=%v want %q", reason, ok, unknownReason))
	}
	//: ReasonOf returned the expected SCREAMING_SNAKE reason.
	return harness.Passed(errsDomain, "reason-of", fmt.Sprintf("ReasonOf=%q", reason))
}

// checkErrsPublicPrivate asserts PublicOf is a non-empty wire-safe message and
// PrivateOf is a non-empty diagnostic message.
func checkErrsPublicPrivate() harness.Result {
	//: produce the pure-Go SDK error under introspection.
	err := codecUnknownFormatErr()
	//: PublicOf is the wire-safe message; it must be non-empty.
	public := errs.PublicOf(err)
	//: PrivateOf is the diagnostic message; it must be non-empty.
	private := errs.PrivateOf(err)
	//: an empty Public or Private breaks the documented split contract.
	if public == "" || private == "" {
		//: report which side was empty for diagnosis.
		return harness.Failed(errsDomain, "public-private", fmt.Sprintf("public=%q private=%q", public, private))
	}
	//: both sides of the Public/Private split are populated (Private redacted).
	return harness.Passed(errsDomain, "public-private", fmt.Sprintf("public=%q (private non-empty, redacted)", public))
}

// checkErrsHasCode asserts HasCode is true for the matching code and false for a
// different one.
func checkErrsHasCode() harness.Result {
	//: produce the pure-Go SDK error under introspection.
	err := codecUnknownFormatErr()
	//: HasCode must match the origin code and reject an unrelated one.
	hit := errs.HasCode(err, codec.CodeUnknownFormat)
	//: a different code from the same package must not match this error.
	miss := errs.HasCode(err, codec.CodeStreamingUnsupported)
	//: a false positive or false negative means code routing is broken.
	if !hit || miss {
		//: report both observations for diagnosis.
		return harness.Failed(errsDomain, "has-code", fmt.Sprintf("hit=%v miss=%v", hit, miss))
	}
	//: HasCode matched the origin code and rejected the unrelated one.
	return harness.Passed(errsDomain, "has-code", "HasCode true for origin, false for other code")
}

// checkErrsHasReason asserts HasReason matches the error's reason and rejects a
// foreign one.
func checkErrsHasReason() harness.Result {
	//: produce the pure-Go SDK error under introspection.
	err := codecUnknownFormatErr()
	//: HasReason must match the origin reason and reject an unrelated one.
	hit := errs.HasReason(err, unknownReason)
	//: a reason from another package must not match this error.
	miss := errs.HasReason(err, foreignReason)
	//: a false positive or false negative means reason routing is broken.
	if !hit || miss {
		//: report both observations for diagnosis.
		return harness.Failed(errsDomain, "has-reason", fmt.Sprintf("hit=%v miss=%v", hit, miss))
	}
	//: HasReason matched the origin reason and rejected the foreign one.
	return harness.Passed(errsDomain, "has-reason", "HasReason true for origin, false for foreign reason")
}

// checkErrsCodeOctets asserts the typed Code's octet accessors decompose the
// dotted-quad as MM.LL.PP.SS (1.2.0.1 for UNKNOWN_FORMAT).
func checkErrsCodeOctets() harness.Result {
	//: produce the pure-Go SDK error under introspection.
	err := codecUnknownFormatErr()
	//: CodeOf yields the typed Code whose octets we decompose.
	code, ok := errs.CodeOf(err)
	//: a missing code means there is nothing to decompose.
	if !ok {
		//: report the absence as the failure detail.
		return harness.Failed(errsDomain, "code-octets", "CodeOf returned ok=false")
	}
	//: each octet must match the registered 1.2.0.1 layout for this code.
	if int(code.Major()) != wantMajor || int(code.Layer()) != wantLayer || int(code.Package()) != wantPkg || int(code.Serial()) != wantSerial {
		//: report the observed octets for diagnosis.
		return harness.Failed(errsDomain, "code-octets", fmt.Sprintf("octets=%d.%d.%d.%d want %d.%d.%d.%d", code.Major(), code.Layer(), code.Package(), code.Serial(), wantMajor, wantLayer, wantPkg, wantSerial))
	}
	//: the octet accessors decomposed the dotted-quad exactly as registered.
	return harness.Passed(errsDomain, "code-octets", fmt.Sprintf("octets=%d.%d.%d.%d", code.Major(), code.Layer(), code.Package(), code.Serial()))
}

// checkErrsExitAndHTTP asserts ExitCodeOf and HTTPStatusOf return sane positive
// values for a real SDK error.
func checkErrsExitAndHTTP() harness.Result {
	//: produce the pure-Go SDK error under introspection.
	err := codecUnknownFormatErr()
	//: ExitCodeOf maps to a POSIX exit code; HTTPStatusOf to an HTTP status.
	exit := errs.ExitCodeOf(err)
	//: HTTPStatusOf must land inside the sane status range.
	status := errs.HTTPStatusOf(err)
	//: a non-positive exit code or out-of-range status is a contract breach.
	if exit <= 0 || status < httpStatusFloor || status > httpStatusCeil {
		//: report both observations for diagnosis.
		return harness.Failed(errsDomain, "exit-http", fmt.Sprintf("exit=%d status=%d", exit, status))
	}
	//: both mappings returned sane values within their documented ranges.
	return harness.Passed(errsDomain, "exit-http", fmt.Sprintf("ExitCodeOf=%d HTTPStatusOf=%d", exit, status))
}

// checkErrsNilDegrades asserts the accessors degrade gracefully on a nil and on
// a plain (non-SDK) error, per the documented defaults.
func checkErrsNilDegrades() harness.Result {
	//: CodeOf(nil) must report (0, false) per the documented contract.
	codeNil, okNil := errs.CodeOf(nil)
	//: CodeOf on a plain error must likewise report (0, false).
	_, okPlain := errs.CodeOf(errPlainNonSDK)
	//: PublicOf must return "" when no SDK error is present.
	publicNil := errs.PublicOf(nil)
	//: the HTTP default is 500 for a non-SDK error.
	statusPlain := errs.HTTPStatusOf(errPlainNonSDK)
	//: the exit default is 70 (EX_SOFTWARE) for a non-SDK error.
	exitPlain := errs.ExitCodeOf(errPlainNonSDK)
	//: any deviation from the documented degraded defaults is a failure.
	if okNil || okPlain || codeNil != 0 || publicNil != "" || statusPlain != httpDefaultStatus || exitPlain != exitDefaultCode {
		//: report every observed value for diagnosis.
		return harness.Failed(errsDomain, "nil-degrades", fmt.Sprintf("okNil=%v okPlain=%v code=%v public=%q status=%d exit=%d", okNil, okPlain, codeNil, publicNil, statusPlain, exitPlain))
	}
	//: nil and plain errors degraded to the documented defaults.
	return harness.Passed(errsDomain, "nil-degrades", fmt.Sprintf("nil/plain to (0,false), public=\"\", status=%d, exit=%d", statusPlain, exitPlain))
}
