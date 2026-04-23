package errs_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

func TestPrefixMatcher_NotAnError(t *testing.T) {
	t.Parallel()
	pm := errs.NewPrefixMatcher(0x01_00_00_00, errs.MaskByMajor)
	//: critical invariant — PrefixMatcher MUST NOT satisfy the error interface,
	//: so it cannot accidentally escape as a function return value.
	if _, ok := any(pm).(error); ok {
		t.Fatalf("PrefixMatcher must NOT implement error; it did")
	}
}

func TestPrefixMatcher_String(t *testing.T) {
	t.Parallel()
	pm := errs.NewPrefixMatcher(0x01_02_00_00, errs.MaskByLayer)
	got := pm.String()
	want := "PrefixMatcher{prefix=1.2.0.0, mask=255.255.0.0}"
	if got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}

func TestPrefixMatcher_Accessors(t *testing.T) {
	t.Parallel()
	pm := errs.NewPrefixMatcher(0x01_00_00_00, errs.MaskByMajor)
	if pm.Prefix() != 0x01_00_00_00 {
		t.Fatalf("Prefix mismatch")
	}
	if pm.Mask() != errs.MaskByMajor {
		t.Fatalf("Mask mismatch")
	}
}
