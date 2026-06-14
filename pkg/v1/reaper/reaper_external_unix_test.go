//go:build unix

// Package reaper — Unix-only facade assertions, gated on the same `unix` build
// tag as the real reaper so the contract is checked on every Unix target rather
// than a runtime.GOOS subset.
package reaper_test

import (
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/reaper"
)

// TestSetChildSubreaperContractUnix asserts that on Unix SetChildSubreaper
// either succeeds (nil) or fails with the central SubreaperFailed sentinel —
// never a raw, untyped error. Non-Linux Unix (darwin/bsd) reports the
// UnsupportedPlatform sentinel, which is also one of the central proc sentinels.
func TestSetChildSubreaperContractUnix(t *testing.T) {
	//: serial — alters this process's subreaper attribute.
	err := reaper.SetChildSubreaper()
	//: success (nil) is a valid outcome on a capable Linux host; a non-Linux Unix
	//: host reports UnsupportedPlatform and a Linux failure reports
	//: SubreaperFailed — every non-nil error must be one of those central
	//: sentinels, never a raw, untyped error.
	switch {
	case err == nil,
		errs.HasCode(err, reaper.SubreaperFailed.Code()),
		errs.HasCode(err, reaper.UnsupportedPlatform.Code()):
		//: success, or one of the central typed sentinels — contract honoured.
		return
	default:
		//: any other error breaks the typed-contract guarantee.
		t.Fatalf("Unix SetChildSubreaper failed with unexpected error: %v", err)
	}
}
