package errs_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

func TestPrefixMatcher_ImplementsErrorForErrorsIs(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		prefix errs.Code
		mask   errs.Code
	}
	tests := []tc{
		{"major-mask matcher implements error", 0x01_00_00_00, errs.MaskByMajor},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		pm := errs.NewPrefixMatcher(c.prefix, c.mask)
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
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestNewPrefixMatcher(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		prefix errs.Code
		mask   errs.Code
	}
	tests := []tc{
		{"major mask", 0x01_00_00_00, errs.MaskByMajor},
		{"layer mask", 0x01_02_00_00, errs.MaskByLayer},
		{"package mask", 0x01_02_03_00, errs.MaskByPackage},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		pm := errs.NewPrefixMatcher(c.prefix, c.mask)
		//: constructor MUST never return nil.
		if pm == nil {
			t.Fatalf("NewPrefixMatcher returned nil")
		}
		//: prefix and mask must round-trip through the accessors verbatim.
		if pm.Prefix() != c.prefix {
			t.Fatalf("Prefix() = %v, want %v", pm.Prefix(), c.prefix)
		}
		if pm.Mask() != c.mask {
			t.Fatalf("Mask() = %v, want %v", pm.Mask(), c.mask)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestPrefixMatcher_String(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		prefix errs.Code
		mask   errs.Code
		want   string
	}
	tests := []tc{
		{"layer mask render", 0x01_02_00_00, errs.MaskByLayer, "PrefixMatcher{prefix=1.2.0.0, mask=255.255.0.0}"},
		{"major mask render", 0x01_00_00_00, errs.MaskByMajor, "PrefixMatcher{prefix=1.0.0.0, mask=255.0.0.0}"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		pm := errs.NewPrefixMatcher(c.prefix, c.mask)
		//: canonical dotted form must remain bit-stable across releases.
		if got := pm.String(); got != c.want {
			t.Fatalf("String() = %q, want %q", got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestPrefixMatcher_Error(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		prefix errs.Code
		mask   errs.Code
	}
	tests := []tc{
		{"Error mirrors String for major mask", 0x01_00_00_00, errs.MaskByMajor},
		{"Error mirrors String for layer mask", 0x02_03_00_00, errs.MaskByLayer},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		pm := errs.NewPrefixMatcher(c.prefix, c.mask)
		//: Error() is a stdlib-protocol concession that delegates to String().
		if pm.Error() != pm.String() {
			t.Fatalf("Error() = %q, want %q", pm.Error(), pm.String())
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestPrefixMatcher_Prefix(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		prefix errs.Code
		mask   errs.Code
	}
	tests := []tc{
		{"major prefix round-trip", 0x01_00_00_00, errs.MaskByMajor},
		{"layer prefix round-trip", 0x01_02_00_00, errs.MaskByLayer},
		{"package prefix round-trip", 0x01_02_03_00, errs.MaskByPackage},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		pm := errs.NewPrefixMatcher(c.prefix, c.mask)
		//: accessor must surface the construction-time value byte-for-byte.
		if pm.Prefix() != c.prefix {
			t.Fatalf("Prefix() = %v, want %v", pm.Prefix(), c.prefix)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestPrefixMatcher_Mask(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		prefix errs.Code
		mask   errs.Code
	}
	tests := []tc{
		{"MaskByMajor round-trip", 0x01_00_00_00, errs.MaskByMajor},
		{"MaskByLayer round-trip", 0x01_02_00_00, errs.MaskByLayer},
		{"MaskByPackage round-trip", 0x01_02_03_00, errs.MaskByPackage},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		pm := errs.NewPrefixMatcher(c.prefix, c.mask)
		//: accessor must surface the construction-time value byte-for-byte.
		if pm.Mask() != c.mask {
			t.Fatalf("Mask() = %v, want %v", pm.Mask(), c.mask)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestPrefixMatcher_Accessors(t *testing.T) {
	t.Parallel()
	type tc struct {
		name       string
		prefix     errs.Code
		mask       errs.Code
		wantPrefix errs.Code
		wantMask   errs.Code
	}
	tests := []tc{
		{
			name:       "major prefix + MaskByMajor round-trip",
			prefix:     0x01_00_00_00,
			mask:       errs.MaskByMajor,
			wantPrefix: 0x01_00_00_00,
			wantMask:   errs.MaskByMajor,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		pm := errs.NewPrefixMatcher(c.prefix, c.mask)
		//: both accessors must surface their construction-time values.
		if pm.Prefix() != c.wantPrefix {
			t.Fatalf("Prefix mismatch: got %v, want %v", pm.Prefix(), c.wantPrefix)
		}
		if pm.Mask() != c.wantMask {
			t.Fatalf("Mask mismatch: got %v, want %v", pm.Mask(), c.wantMask)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
