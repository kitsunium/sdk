package errs_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

func TestCodeString_Canonical(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   errs.Code
		want string
	}
	tests := []tc{
		{"zero origin",        0x00_00_00_00, "0.0.0.0"},
		{"meta invalid code",  0x00_00_00_01, "0.0.0.1"},
		{"kernel errs slot 3", 0x00_00_00_03, "0.0.0.3"},
		{"core codec",         0x00_02_02_01, "0.2.2.1"},
		{"service json",       0x00_03_02_01, "0.3.2.1"},
		{"v1 logger",          0x01_01_00_01, "1.1.0.1"},
		{"max packed",         0xFF_FF_FF_FF, "255.255.255.255"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := c.in.String()
		if got != c.want {
			t.Fatalf("%s: %#08x.String() = %q, want %q", c.name, uint32(c.in), got, c.want)
		}
	}
	for _, c := range tests {
		c := c
		t.Run(c.name, func(t *testing.T) { t.Parallel(); runCase(t, c) })
	}
}

func TestCodePadded_DisplayOnly(t *testing.T) {
	t.Parallel()
	var c errs.Code = 0x00_03_02_01
	if got, want := c.Padded(), "000.003.002.001"; got != want {
		t.Fatalf("Padded() = %q, want %q", got, want)
	}
	if c.String() == c.Padded() {
		t.Fatalf("String() and Padded() must differ for non-trivial codes")
	}
	//: asserting the canonical form is shorter (sanity check).
	if len(c.String()) >= len(c.Padded()) {
		t.Fatalf("canonical String() should be shorter than Padded()")
	}
}

func TestMasks_PackingSanity(t *testing.T) {
	t.Parallel()
	c := errs.Pack(1, 2, 3, 4)
	if c&errs.MaskByMajor != errs.Code(1)<<24 {
		t.Fatalf("MaskByMajor extraction failed")
	}
	if c&errs.MaskByLayer != errs.Code(1)<<24|errs.Code(2)<<16 {
		t.Fatalf("MaskByLayer extraction failed")
	}
	if c&errs.MaskExact != c {
		t.Fatalf("MaskExact should be identity")
	}
}
