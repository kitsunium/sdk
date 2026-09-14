// Package checks — hosts the errs-domain conformance suite: dotted-quad code
// and reason introspection, the Public/Private split, prefix matching, and the
// documented degradation of every accessor on a non-SDK error.
package checks

import (
	"errors"
	"fmt"

	"github.com/kitsunium/sdk/e2e/harness"
	"github.com/kitsunium/sdk/pkg/v1/codec"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// errsDomain labels every errs-suite Result.
const errsDomain = "errs"

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
