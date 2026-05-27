package logger_test

import (
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/logger"
)

// TestFrameworkVersionInjected covers the link-time-set arm of
// FrameworkVersion: when Version is non-empty (as the ldflags / Bazel
// --stamp pipeline leaves it in a real build) the function returns it
// verbatim instead of the "dev" sentinel. The package global is mutated
// here, so this test is intentionally NOT parallel — it runs to completion
// (set → assert → restore) while every t.Parallel() test in the package is
// still paused, keeping the shared Version free of races.
func TestFrameworkVersionInjected(t *testing.T) {
	//: snapshot the link-time value so the mutation is fully reversible.
	saved := logger.Version
	//: restore on exit regardless of assertion outcome.
	t.Cleanup(func() { logger.Version = saved })
	tests := []struct {
		name string
		set  string
	}{
		{"semver", "v1.2.3"},
		{"arbitrary", "build-42-abcdef"},
	}
	runCase := func(t *testing.T, set string) {
		t.Helper()
		//: stamp the global as the build pipeline would.
		logger.Version = set
		//: the injected value must come back unchanged, not "dev".
		if got := logger.FrameworkVersion(); got != set {
			t.Errorf("FrameworkVersion()=%q want %q", got, set)
		}
	}
	for _, tc := range tests {
		//: subtests stay serial — they share the package global Version.
		t.Run(tc.name, func(t *testing.T) {
			runCase(t, tc.set)
		})
	}
}

func TestFrameworkVersionFallback(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"returns non-empty sentinel in dev"},
		{"never returns empty string"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := logger.FrameworkVersion()
			if got == "" {
				t.Errorf("FrameworkVersion = empty")
			}
		})
	}
}
