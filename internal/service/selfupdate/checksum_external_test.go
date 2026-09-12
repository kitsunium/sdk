// Package updater_test provides black-box tests for the public checksum
// integrity surface (CWE-494): the exported sentinel errors that callers
// classify with errors.Is to distinguish a refused update from other failures.
package selfupdate_test

import (
	"errors"
	"fmt"
	"testing"

	coreupd "github.com/kitsunium/sdk/internal/core/selfupdate"
)

// TestChecksumSentinelsSurviveWrapping pins the public error contract for the
// integrity sentinels exported from checksum.go: each one stays classifiable
// with errors.Is after being wrapped with %w, which is exactly how the upgrade
// pipeline surfaces them to callers (the CWE-494 refusal classification).
func TestChecksumSentinelsSurviveWrapping(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		sentinel error
	}{
		{name: "missing", sentinel: coreupd.ChecksumMissing},
		{name: "mismatch", sentinel: coreupd.ChecksumMismatch},
		{name: "too large", sentinel: coreupd.ArchiveTooLarge},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: A nil sentinel would silently break every errors.Is call site.
			if tc.sentinel == nil {
				t.Fatalf("sentinel %q is nil", tc.name)
			}
			//: Wrap the way the real pipeline does, then require classification
			//: to still succeed — a computed round-trip, not a constant compare.
			wrapped := fmt.Errorf("asset foo (tag v1): %w", tc.sentinel)
			if !errors.Is(wrapped, tc.sentinel) {
				t.Errorf("errors.Is(wrapped, %s) = false, want true", tc.name)
			}
		})
	}
}

// TestChecksumSentinelsMutuallyDistinct guards against two integrity
// sentinels being accidentally aliased to the same value, which would
// collapse errors.Is classification at every call site.
func TestChecksumSentinelsMutuallyDistinct(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		a    error
		b    error
	}{
		{name: "missing vs mismatch", a: coreupd.ChecksumMissing, b: coreupd.ChecksumMismatch},
		{name: "missing vs too large", a: coreupd.ChecksumMissing, b: coreupd.ArchiveTooLarge},
		{name: "mismatch vs too large", a: coreupd.ChecksumMismatch, b: coreupd.ArchiveTooLarge},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: Distinct sentinels must never classify as one another.
			if errors.Is(tc.a, tc.b) {
				t.Errorf("errors.Is(%s) = true, want false — sentinels aliased", tc.name)
			}
		})
	}
}
