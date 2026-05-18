package errs_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

func TestCode_String(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   errs.Code
		want string
	}
	tests := []tc{
		{"zero origin", 0x00_00_00_00, "0.0.0.0"},
		{"meta invalid code", 0x00_00_00_01, "0.0.0.1"},
		{"kernel errs slot 3", 0x00_00_00_03, "0.0.0.3"},
		{"core codec", 0x00_02_02_01, "0.2.2.1"},
		{"service json", 0x00_03_02_01, "0.3.2.1"},
		{"v1 logger", 0x01_01_00_01, "1.1.0.1"},
		{"max packed", 0xFF_FF_FF_FF, "255.255.255.255"},
	}
	runCase := func(t *testing.T, tcase tc) {
		t.Helper()
		got := tcase.in.String()
		if got != tcase.want {
			t.Fatalf("%s: %#08x.String() = %q, want %q",
				tcase.name, uint32(tcase.in), got, tcase.want)
		}
	}
	for _, tcase := range tests {
		t.Run(tcase.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tcase)
		})
	}
}

func TestCode_Padded(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   errs.Code
		want string
	}
	tests := []tc{
		{"zero origin", 0x00_00_00_00, "000.000.000.000"},
		{"meta invalid code", 0x00_00_00_01, "000.000.000.001"},
		{"kernel errs slot 3", 0x00_00_00_03, "000.000.000.003"},
		{"service json", 0x00_03_02_01, "000.003.002.001"},
		{"v1 logger", 0x01_01_00_01, "001.001.000.001"},
		{"max packed", 0xFF_FF_FF_FF, "255.255.255.255"},
	}
	runCase := func(t *testing.T, tcase tc) {
		t.Helper()
		got := tcase.in.Padded()
		if got != tcase.want {
			t.Fatalf("%s: %#08x.Padded() = %q, want %q",
				tcase.name, uint32(tcase.in), got, tcase.want)
		}
		//: padded form is constant width per ADR 0005 display contract.
		const paddedWidth = len("255.255.255.255")
		if len(got) != paddedWidth {
			t.Fatalf("%s: Padded() length = %d, want %d", tcase.name, len(got), paddedWidth)
		}
		//: canonical and padded forms must agree on max-octet boundary only.
		if tcase.in != 0xFF_FF_FF_FF && tcase.in.String() == got {
			t.Fatalf("%s: String() and Padded() must differ for non-max codes", tcase.name)
		}
	}
	for _, tcase := range tests {
		t.Run(tcase.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tcase)
		})
	}
}

func TestPack(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		mm   errs.Major
		ll   errs.Layer
		pp   errs.PkgCode
		ss   errs.Serial
		want errs.Code
	}
	tests := []tc{
		{"meta invalid code", 0, 0, 0, 1, 0x00_00_00_01},
		{"kernel buffer", 0, 1, 1, 0, 0x00_01_01_00},
		{"v1 logger", 1, 1, 0, 1, 0x01_01_00_01},
		{"max octets", 255, 255, 255, 255, 0xFF_FF_FF_FF},
	}
	runCase := func(t *testing.T, tcase tc) {
		t.Helper()
		got := errs.Pack(tcase.mm, tcase.ll, tcase.pp, tcase.ss)
		if got != tcase.want {
			t.Fatalf("%s: Pack(%d,%d,%d,%d) = %#08x, want %#08x",
				tcase.name, tcase.mm, tcase.ll, tcase.pp, tcase.ss,
				uint32(got), uint32(tcase.want))
		}
	}
	for _, tcase := range tests {
		t.Run(tcase.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tcase)
		})
	}
}

func TestCode_Major(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   errs.Code
		want errs.Major
	}
	tests := []tc{
		{"zero", 0x00_00_00_00, 0},
		{"v1 logger", 0x01_01_00_01, 1},
		{"max", 0xFF_FF_FF_FF, 255},
	}
	runCase := func(t *testing.T, tcase tc) {
		t.Helper()
		if got := tcase.in.Major(); got != tcase.want {
			t.Fatalf("%s: %#08x.Major() = %d, want %d",
				tcase.name, uint32(tcase.in), got, tcase.want)
		}
	}
	for _, tcase := range tests {
		t.Run(tcase.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tcase)
		})
	}
}

func TestCode_Layer(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   errs.Code
		want errs.Layer
	}
	tests := []tc{
		{"zero", 0x00_00_00_00, 0},
		{"kernel layer", 0x00_01_00_00, 1},
		{"v1 logger layer", 0x01_01_00_01, 1},
		{"max", 0xFF_FF_FF_FF, 255},
	}
	runCase := func(t *testing.T, tcase tc) {
		t.Helper()
		if got := tcase.in.Layer(); got != tcase.want {
			t.Fatalf("%s: %#08x.Layer() = %d, want %d",
				tcase.name, uint32(tcase.in), got, tcase.want)
		}
	}
	for _, tcase := range tests {
		t.Run(tcase.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tcase)
		})
	}
}

func TestCode_Package(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   errs.Code
		want errs.PkgCode
	}
	tests := []tc{
		{"zero", 0x00_00_00_00, 0},
		{"package slot 2", 0x00_02_02_01, 2},
		{"v1 logger pkg 0", 0x01_01_00_01, 0},
		{"max", 0xFF_FF_FF_FF, 255},
	}
	runCase := func(t *testing.T, tcase tc) {
		t.Helper()
		if got := tcase.in.Package(); got != tcase.want {
			t.Fatalf("%s: %#08x.Package() = %d, want %d",
				tcase.name, uint32(tcase.in), got, tcase.want)
		}
	}
	for _, tcase := range tests {
		t.Run(tcase.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tcase)
		})
	}
}

func TestCode_Serial(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   errs.Code
		want errs.Serial
	}
	tests := []tc{
		{"zero", 0x00_00_00_00, 0},
		{"meta invalid code", 0x00_00_00_01, 1},
		{"v1 logger serial 1", 0x01_01_00_01, 1},
		{"max", 0xFF_FF_FF_FF, 255},
	}
	runCase := func(t *testing.T, tcase tc) {
		t.Helper()
		if got := tcase.in.Serial(); got != tcase.want {
			t.Fatalf("%s: %#08x.Serial() = %d, want %d",
				tcase.name, uint32(tcase.in), got, tcase.want)
		}
	}
	for _, tcase := range tests {
		t.Run(tcase.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tcase)
		})
	}
}

func TestMasks_PackingSanity(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		mask errs.Code
		in   errs.Code
		want errs.Code
	}
	const (
		majorByte    errs.Code = 1
		layerByte    errs.Code = 2
		packageByte  errs.Code = 3
		serialByte   errs.Code = 4
		shiftMajor   uint      = 24
		shiftLayer   uint      = 16
		shiftPackage uint      = 8
	)
	packed := errs.Pack(errs.Major(majorByte), errs.Layer(layerByte), errs.PkgCode(packageByte), errs.Serial(serialByte))
	tests := []tc{
		{
			name: "mask by major isolates top octet",
			mask: errs.MaskByMajor,
			in:   packed,
			want: majorByte << shiftMajor,
		},
		{
			name: "mask by layer keeps major and layer",
			mask: errs.MaskByLayer,
			in:   packed,
			want: majorByte<<shiftMajor | layerByte<<shiftLayer,
		},
		{
			name: "mask by package keeps major, layer, package",
			mask: errs.MaskByPackage,
			in:   packed,
			want: majorByte<<shiftMajor | layerByte<<shiftLayer | packageByte<<shiftPackage,
		},
		{
			name: "exact mask is identity",
			mask: errs.MaskExact,
			in:   packed,
			want: packed,
		},
	}
	runCase := func(t *testing.T, tcase tc) {
		t.Helper()
		if got := tcase.in & tcase.mask; got != tcase.want {
			t.Fatalf("%s: %#08x & %#08x = %#08x, want %#08x",
				tcase.name, uint32(tcase.in), uint32(tcase.mask), uint32(got), uint32(tcase.want))
		}
	}
	for _, tcase := range tests {
		t.Run(tcase.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tcase)
		})
	}
}
