package errs_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

func TestPrefixMatcher_ImplementsErrorForErrorsIs(t *testing.T) {
	t.Parallel()
	pm := errs.NewPrefixMatcher(0x01_00_00_00, errs.MaskByMajor)
	//: PrefixMatcher MUST implement error so it is usable as an errors.Is
	//: target. Escape-as-return-value is discouraged by convention +
	//: follow-up linter rule, not by type system.
	if _, ok := any(pm).(error); !ok {
		t.Fatalf("PrefixMatcher must implement error for errors.Is protocol")
	}
	//: Error() is diagnostic — same string as String().
	if got := pm.Error(); got != pm.String() {
		t.Fatalf("Error() should return String(): got %q vs %q", got, pm.String())
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
