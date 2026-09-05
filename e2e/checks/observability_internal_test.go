// Package checks — the logger and errs conformance checks.
package checks

import (
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/logger"
)

// Test_newBufferLogger pins the fixture every logger check is built on.
//
// The checks assert on what the logger WROTE, so a fixture that handed back a
// logger writing somewhere else — or a buffer the logger does not write to —
// would make every one of them pass on an empty string. That is the one way
// this domain can report a clean run while proving nothing.
func Test_newBufferLogger(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// min is the level floor the fixture is built with.
		min logger.Level
		// emit writes one line at the level under test.
		emit func(lg logger.Logger)
		// wantWritten is whether the line must reach the buffer.
		wantWritten bool
	}
	info := func(lg logger.Logger) { logger.Info(t.Context(), lg, "conformance-fixture-probe") }
	debug := func(lg logger.Logger) { logger.Debug(t.Context(), lg, "conformance-fixture-probe") }
	fail := func(lg logger.Logger) { logger.Error(t.Context(), lg, "conformance-fixture-probe") }
	tests := []tc{
		{name: "a line at the floor", min: logger.LevelInfo, emit: info, wantWritten: true},
		{name: "a line above the floor", min: logger.LevelInfo, emit: fail, wantWritten: true},
		//: a line below the floor is filtered, which is what the level check
		//: relies on being true.
		{name: "a line below the floor", min: logger.LevelWarn, emit: debug},
		{name: "a debug floor admits debug", min: logger.LevelDebug, emit: debug, wantWritten: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		buf, lg, err := newBufferLogger(c.min)
		if err != nil {
			t.Fatalf("newBufferLogger(%v) = %v", c.min, err)
		}
		if buf == nil || lg == nil {
			t.Fatal("the fixture handed back no buffer or no logger")
		}
		//: a fresh fixture has recorded nothing, so anything found below was
		//: written by the line under test.
		if buf.Len() != 0 {
			t.Fatalf("the fixture buffer already holds %d bytes", buf.Len())
		}

		c.emit(lg)

		written := buf.Len() > 0
		if written != c.wantWritten {
			t.Fatalf("the line reached the buffer = %v, want %v — the checks assert "+
				"on what the logger wrote, so a fixture that writes elsewhere makes "+
				"every one of them pass on an empty string", written, c.wantWritten)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_codecUnknownFormatErr pins the fixture the errs checks are built on: a
// real, SDK-produced error rather than a hand-made one.
//
// The point of the errs domain is that the SDK's own errors carry a code, a
// reason and a public/private split. An error fabricated by the test would prove
// the assertions work and nothing about the SDK, so the fixture asks a real
// package for a real failure.
func Test_codecUnknownFormatErr(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// runs is how many times the fixture is asked for an error.
		runs int
	}
	tests := []tc{
		{name: "one error", runs: 1},
		//: the errs checks each ask for their own, so it has to be repeatable.
		{name: "several errors", runs: 4},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		for i := range c.runs {
			got := codecUnknownFormatErr()

			//: a nil here would make every errs check assert against nothing.
			if got == nil {
				t.Fatalf("run %d produced no error — the errs checks would have "+
					"nothing to interrogate", i)
			}
			if got.Error() == "" {
				t.Errorf("run %d produced an error with no message", i)
			}
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// The logger and errs checks are pure — no kernel facility, no permission — so
// they can be exercised here as well as in the conformance binary. The logger is
// also the one domain whose failure is silent by construction: a logger that
// writes nothing looks exactly like a quiet run.

// Test_checkLoggerTextToBuffer pins that a record's message text actually
// reaches the sink. A logger that drops every record is indistinguishable from
// one nobody called.
func Test_checkLoggerTextToBuffer(t *testing.T) {
	t.Parallel()
	if problem := passingRow(checkLoggerTextToBuffer(), loggerDomain); problem != nil {
		t.Fatal(problem)
	}
}

// Test_checkLoggerAttributes pins that typed attributes render as key=value
// rather than being dropped or stringified into the message.
func Test_checkLoggerAttributes(t *testing.T) {
	t.Parallel()
	if problem := passingRow(checkLoggerAttributes(), loggerDomain); problem != nil {
		t.Fatal(problem)
	}
}

// Test_checkLoggerFrameworkVersion pins the auto-decoration every record carries.
// It is what lets an operator tell which SDK version produced a line months
// later, and nothing else in the pipeline adds it.
func Test_checkLoggerFrameworkVersion(t *testing.T) {
	t.Parallel()
	if problem := passingRow(checkLoggerFrameworkVersion(), loggerDomain); problem != nil {
		t.Fatal(problem)
	}
}

// Test_checkLoggerLevelFilter pins that the floor actually filters. A filter that
// admits everything is invisible until a debug line carrying a secret reaches a
// production sink.
func Test_checkLoggerLevelFilter(t *testing.T) {
	t.Parallel()
	if problem := passingRow(checkLoggerLevelFilter(), loggerDomain); problem != nil {
		t.Fatal(problem)
	}
}

// Test_checkLoggerBuilder pins the builder path, which is how a consumer
// assembles a logger without the constructor's full config.
func Test_checkLoggerBuilder(t *testing.T) {
	t.Parallel()
	if problem := passingRow(checkLoggerBuilder(), loggerDomain); problem != nil {
		t.Fatal(problem)
	}
}

// Test_checkLoggerDefault pins that the package default is usable as shipped —
// the logger a consumer gets before configuring anything.
func Test_checkLoggerDefault(t *testing.T) {
	t.Parallel()
	if problem := passingRow(checkLoggerDefault(), loggerDomain); problem != nil {
		t.Fatal(problem)
	}
}

// Test_checkErrsCode pins the dotted-quad code every SDK error carries. It is
// what the whole error model is addressed by, so a code that does not survive
// wrapping makes every matcher downstream unreliable.
func Test_checkErrsCode(t *testing.T) {
	t.Parallel()
	if problem := passingRow(checkErrsCode(), errsDomain); problem != nil {
		t.Fatal(problem)
	}
}

// Test_checkErrsReason pins the machine-readable reason, which is what a caller
// branches on when the code alone is too coarse.
func Test_checkErrsReason(t *testing.T) {
	t.Parallel()
	if problem := passingRow(checkErrsReason(), errsDomain); problem != nil {
		t.Fatal(problem)
	}
}

// Test_checkErrsPublicPrivate pins the split that keeps internals out of a
// response body. A public message that leaked the private one would be invisible
// in a test that only reads Error().
func Test_checkErrsPublicPrivate(t *testing.T) {
	t.Parallel()
	if problem := passingRow(checkErrsPublicPrivate(), errsDomain); problem != nil {
		t.Fatal(problem)
	}
}

// Test_checkErrsHasCode pins the code matcher through a wrap, which is the only
// form callers meet it in.
func Test_checkErrsHasCode(t *testing.T) {
	t.Parallel()
	if problem := passingRow(checkErrsHasCode(), errsDomain); problem != nil {
		t.Fatal(problem)
	}
}

// Test_checkErrsHasReason pins the reason matcher on the same terms.
func Test_checkErrsHasReason(t *testing.T) {
	t.Parallel()
	if problem := passingRow(checkErrsHasReason(), errsDomain); problem != nil {
		t.Fatal(problem)
	}
}

// Test_checkErrsCodeOctets pins that a code decomposes back into the four octets
// it was built from — the property that makes a code readable as
// module.layer.package.symbol rather than as an opaque number.
func Test_checkErrsCodeOctets(t *testing.T) {
	t.Parallel()
	if problem := passingRow(checkErrsCodeOctets(), errsDomain); problem != nil {
		t.Fatal(problem)
	}
}

// Test_checkErrsExitAndHTTP pins the two mappings a boundary needs: a process
// exit status and an HTTP status. Both are how an error leaves the program.
func Test_checkErrsExitAndHTTP(t *testing.T) {
	t.Parallel()
	if problem := passingRow(checkErrsExitAndHTTP(), errsDomain); problem != nil {
		t.Fatal(problem)
	}
}

// Test_checkErrsNilDegrades pins that every accessor tolerates a nil error.
// A matcher that panicked on nil would turn a successful path into a crash at
// exactly the moment nothing went wrong.
func Test_checkErrsNilDegrades(t *testing.T) {
	t.Parallel()
	if problem := passingRow(checkErrsNilDegrades(), errsDomain); problem != nil {
		t.Fatal(problem)
	}
}
