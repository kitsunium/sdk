//go:build !unix

// Package reaper — non-Unix facade assertions, gated on the same `!unix` build
// tag as the no-op reaper so the contract is verified on every non-Unix target
// (windows/plan9/js/wasip1), not just Windows via a runtime.GOOS guess.
package reaper_test

import (
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/reaper"
)

// TestSetChildSubreaperContractNonUnix asserts that on every non-Unix platform
// SetChildSubreaper returns exactly the central UnsupportedPlatform sentinel —
// the no-op stub must degrade to a typed error, never act and never panic.
func TestSetChildSubreaperContractNonUnix(t *testing.T) {
	t.Parallel()
	//: the no-op stub must always report the platform sentinel off Unix.
	err := reaper.SetChildSubreaper()
	//: a nil result would mean the stub silently claimed success — a contract
	//: violation on a platform with no prctl equivalent.
	if err == nil {
		t.Fatalf("non-Unix SetChildSubreaper = nil, want UnsupportedPlatform")
	}
	//: the only permitted error off Unix is the central platform sentinel.
	if !errs.HasCode(err, reaper.UnsupportedPlatform.Code()) {
		t.Fatalf("non-Unix SetChildSubreaper = %v, want UnsupportedPlatform", err)
	}
}
