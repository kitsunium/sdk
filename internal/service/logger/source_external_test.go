// Package logger_test — black-box tests for the WithCaller entry point.
package logger_test

import (
	"bytes"
	"strings"
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
	svclogger "github.com/kitsunium/sdk/internal/service/logger"
)

// buildLogger wires a Logger over the legacy text handler writing into buf so
// the test can read the encoded line after a Log call. NewTextHandler is the
// fused format+transport handler that needs only an io.Writer and a level.
func buildLogger(t *testing.T, buf *bytes.Buffer) corelogger.Logger {
	t.Helper()

	//: build a text handler writing into the observable buffer at debug level.
	h, err := svclogger.NewTextHandler(buf, level.Debug)
	//: guard handler construction before wrapping into a Logger.
	if err != nil {
		t.Fatalf("NewTextHandler: %v", err)
	}

	//: wrap the handler into a Logger.
	lg, err := svclogger.New(h)
	//: guard Logger construction before returning.
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return lg
}

// Test_WithCaller verifies the wrapped Logger emits a source attribute and that
// a nil logger is returned unchanged.
func Test_WithCaller(t *testing.T) {
	t.Parallel()

	//: table of scenarios spanning the nil passthrough and the emit assertion.
	tests := []struct {
		name       string
		nilLogger  bool
		wantSource bool
	}{
		{name: "nil logger returned unchanged", nilLogger: true, wantSource: false},
		{name: "wrapped logger emits source", nilLogger: false, wantSource: true},
	}

	//: exercise each scenario in its own subtest.
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			//: the nil-logger path asserts the fail-safe passthrough.
			if tc.nilLogger {
				//: a nil input must come back nil rather than a live wrapper.
				if got := svclogger.WithCaller(nil, 0); got != nil {
					t.Fatalf("expected nil, got %T", got)
				}
				return
			}

			//: build a real Logger over an observable buffer.
			var buf bytes.Buffer
			lg := buildLogger(t, &buf)

			//: wrap it so emitted records carry the source annotation.
			wrapped := svclogger.WithCaller(lg, 0)

			//: emit one record through the wrapped Logger.
			wrapped.Log(t.Context(), level.Info, "hello")

			//: the encoded line must carry the source key when wrapping is on.
			if got := strings.Contains(buf.String(), svclogger.SourceFieldKey); got != tc.wantSource {
				t.Fatalf("source present=%v want %v in %q", got, tc.wantSource, buf.String())
			}
		})
	}
}

// Test_WithCaller_skipVariations verifies the resolved source names this test
// file regardless of the skip offset supplied (the captured PC is fixed).
func Test_WithCaller_skipVariations(t *testing.T) {
	t.Parallel()

	//: table of skip offsets that must all resolve to this call site.
	tests := []struct {
		name string
		skip int
	}{
		{name: "skip zero", skip: 0},
		{name: "skip one", skip: 1},
		{name: "negative skip clamped", skip: -3},
	}

	//: emit through a wrapped Logger at each skip and inspect the line.
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			//: build a real Logger over an observable buffer.
			var buf bytes.Buffer
			lg := buildLogger(t, &buf)

			//: wrap with the skip offset under test.
			wrapped := svclogger.WithCaller(lg, tc.skip)

			//: emit one record so the source attr is resolved and rendered.
			wrapped.Log(t.Context(), level.Info, "hello")

			//: the rendered line must reference this external test file.
			if !strings.Contains(buf.String(), "source_external_test.go") {
				t.Fatalf("expected source_external_test.go in %q", buf.String())
			}
		})
	}
}
