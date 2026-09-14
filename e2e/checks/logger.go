// Package checks holds the per-domain conformance suites for the SDK e2e binary.
package checks

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/kitsunium/sdk/e2e/harness"
	"github.com/kitsunium/sdk/pkg/v1/logger"
)

// loggerDomain labels every logger-suite Result.
const loggerDomain = "logger"

// frameworkVersionKey is the auto-injected attribute every emitted record carries.
const frameworkVersionKey = "framework_version"

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
