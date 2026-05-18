package errs

import "testing"

func Test_Pack_RoundTrip(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		mm   Major
		ll   Layer
		pp   PkgCode
		ss   Serial
		want Code
	}
	tests := []tc{
		{"meta InvalidCode", 0, 0, 0, 1, 0x00_00_00_01},
		{"kernel buffer", 0, 1, 1, 0, 0x00_01_01_00},
		{"v1 logger", 1, 1, 0, 1, 0x01_01_00_01},
		{"max octets", 255, 255, 255, 255, 0xFF_FF_FF_FF},
	}
	runCase := func(t *testing.T, tcase tc) {
		t.Helper()
		got := Pack(tcase.mm, tcase.ll, tcase.pp, tcase.ss)
		if got != tcase.want {
			t.Fatalf("%s: Pack(%d,%d,%d,%d) = %#08x, want %#08x",
				tcase.name, tcase.mm, tcase.ll, tcase.pp, tcase.ss, uint32(got), uint32(tcase.want))
		}
		if got.Major() != tcase.mm || got.Layer() != tcase.ll || got.Package() != tcase.pp || got.Serial() != tcase.ss {
			t.Fatalf("%s: accessor round-trip mismatch", tcase.name)
		}
	}
	for _, tcase := range tests {
		t.Run(tcase.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tcase)
		})
	}
}

func Test_Comparability_SwitchAndMapKey(t *testing.T) {
	t.Parallel()
	const (
		codeAlpha Code = 0x00_00_00_01
		codeBeta  Code = 0x00_00_00_02
	)
	type tc struct {
		name string
		run  func(t *testing.T)
	}
	tests := []tc{
		{
			name: "switch matches identical Code",
			run: func(t *testing.T) {
				t.Helper()
				//: switch on Code compiles and works.
				switch codeAlpha {
				//: expected branch — same identifier returns the matching case.
				case codeAlpha:
					return
				//: defensive default — failure path if dispatch is broken.
				default:
					t.Fatalf("switch did not match identical Code")
				}
			},
		},
		{
			name: "map-key usage compiles and works",
			run: func(t *testing.T) {
				t.Helper()
				//: map-key usage compiles and works.
				lookup := map[Code]string{codeAlpha: "a", codeBeta: "b"}
				if lookup[codeAlpha] != "a" || lookup[codeBeta] != "b" {
					t.Fatalf("map-key usage failed: %+v", lookup)
				}
			},
		},
		{
			name: "ordering is well-defined",
			run: func(t *testing.T) {
				t.Helper()
				//: ordering is well-defined (Code is a numeric type).
				if codeAlpha >= codeBeta {
					t.Fatalf("expected codeAlpha < codeBeta, got %#x >= %#x",
						uint32(codeAlpha), uint32(codeBeta))
				}
			},
		},
	}
	for _, tcase := range tests {
		t.Run(tcase.name, func(t *testing.T) {
			t.Parallel()
			tcase.run(t)
		})
	}
}

func Test_itoaDecimal(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		input uint8
		want  string
	}
	tests := []tc{
		{"zero", 0, "0"},
		{"single digit", 5, "5"},
		{"low double digit", 10, "10"},
		{"high double digit", 99, "99"},
		{"low triple digit", 100, "100"},
		{"max octet", 255, "255"},
	}
	for _, tcase := range tests {
		t.Run(tcase.name, func(t *testing.T) {
			t.Parallel()
			if got := itoaDecimal(tcase.input); got != tcase.want {
				t.Errorf("itoaDecimal(%d) = %q, want %q", tcase.input, got, tcase.want)
			}
		})
	}
}

func Test_itoaPadded3(t *testing.T) {
	t.Parallel()
	const expectedWidth int = 3
	type tc struct {
		name  string
		input uint8
		want  string
	}
	tests := []tc{
		{"zero", 0, "000"},
		{"single digit", 5, "005"},
		{"low double digit", 10, "010"},
		{"high double digit", 99, "099"},
		{"low triple digit", 100, "100"},
		{"max octet", 255, "255"},
	}
	for _, tcase := range tests {
		t.Run(tcase.name, func(t *testing.T) {
			t.Parallel()
			got := itoaPadded3(tcase.input)
			if got != tcase.want {
				t.Errorf("itoaPadded3(%d) = %q, want %q", tcase.input, got, tcase.want)
			}
			if len(got) != expectedWidth {
				t.Errorf("itoaPadded3(%d) not length %d: %q", tcase.input, expectedWidth, got)
			}
		})
	}
}
