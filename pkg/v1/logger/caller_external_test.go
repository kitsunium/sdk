// Package logger_test — black-box tests for the public WithCaller facade.
package logger_test

import (
	"strings"
	"testing"

	logger "github.com/kitsunium/sdk/pkg/v1/logger"
)

// Test_WithCaller verifies the public facade derives a Logger whose records
// carry a source annotation and that a nil logger is returned unchanged.
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
				if got := logger.WithCaller(nil, 0); got != nil {
					t.Fatalf("expected nil, got %T", got)
				}
				return
			}

			//: build a real text Logger over an observable buffer.
			var buf strings.Builder
			lg, err := logger.NewText(logger.Config{Writer: &buf, MinLevel: logger.LevelDebug})
			//: guard construction before wrapping.
			if err != nil {
				t.Fatalf("NewText: %v", err)
			}

			//: wrap so emitted records carry the source annotation.
			wrapped := logger.WithCaller(lg, 0)

			//: emit one record through the wrapped Logger.
			logger.Info(t.Context(), wrapped, "hello")

			//: the encoded line must carry the source key when wrapping is on.
			if got := strings.Contains(buf.String(), "source"); got != tc.wantSource {
				t.Fatalf("source present=%v want %v in %q", got, tc.wantSource, buf.String())
			}
		})
	}
}

// Test_WithCaller_resolvesCallSite verifies the rendered source carries a
// concrete Go file:line location. The captured PC points at the frame that
// called Logger.Log — here the package Info helper — so the assertion checks
// for a resolved ".go:" location under the source key rather than a specific
// file (the emission helper, not the test, owns the captured frame).
func Test_WithCaller_resolvesCallSite(t *testing.T) {
	t.Parallel()

	//: table describing the single call-site resolution invariant.
	tests := []struct {
		name string
	}{
		{name: "source carries a resolved go file location"},
	}

	//: exercise each case in its own subtest.
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			//: build a real text Logger over an observable buffer.
			var buf strings.Builder
			lg, err := logger.NewText(logger.Config{Writer: &buf, MinLevel: logger.LevelDebug})
			//: guard construction before wrapping.
			if err != nil {
				t.Fatalf("NewText: %v", err)
			}

			//: wrap so the source attr is resolved at emit time.
			wrapped := logger.WithCaller(lg, 0)

			//: emit one record so the source attr is rendered into the buffer.
			logger.Info(t.Context(), wrapped, "hello")

			//: the rendered line must carry the source key.
			if !strings.Contains(buf.String(), "source") {
				t.Fatalf("expected source key in %q", buf.String())
			}

			//: the source value must carry a resolved go file:line location.
			if !strings.Contains(buf.String(), ".go:") {
				t.Fatalf("expected a resolved .go: location in %q", buf.String())
			}
		})
	}
}
