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

func Test_appendPadded3(t *testing.T) {
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
			got := string(appendPadded3(nil, tcase.input))
			if got != tcase.want {
				t.Errorf("appendPadded3(%d) = %q, want %q", tcase.input, got, tcase.want)
			}
			if len(got) != expectedWidth {
				t.Errorf("appendPadded3(%d) not length %d: %q", tcase.input, expectedWidth, got)
			}
		})
	}
}

// Test_octetRenderingsAgree sweeps every octet value: appendCode's digits are
// strconv's, codeLen is the length String returns, and Padded is the unpadded
// form left-filled to three digits — so the renderers that write a code in
// place cannot drift from the canonical form.
func Test_octetRenderingsAgree(t *testing.T) {
	t.Parallel()
	for octet := range 256 {
		o := uint8(octet)
		want := itoaDecimal(o)
		code := Pack(Major(o), Layer(o), PkgCode(o), Serial(o))
		dotted := want + "." + want + "." + want + "." + want
		if got := code.String(); got != dotted {
			t.Fatalf("Code(%d…).String() = %q, want %q", o, got, dotted)
		}
		if got := codeLen(code); got != len(dotted) {
			t.Fatalf("codeLen(%d…) = %d, want %d", o, got, len(dotted))
		}
		padded := string(appendPadded3(nil, o))
		for len(want) < len(padded) {
			want = "0" + want
		}
		if padded != want {
			t.Fatalf("appendPadded3(%d) = %q, want %q", o, padded, want)
		}
		if got := code.Padded(); got != padded+"."+padded+"."+padded+"."+padded {
			t.Fatalf("Code(%d…).Padded() = %q", o, got)
		}
	}
}
