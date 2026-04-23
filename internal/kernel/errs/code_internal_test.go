package errs

import "testing"

func Test_Pack_RoundTrip(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		m    Major
		l    Layer
		p    PkgCode
		s    Serial
		want Code
	}
	tests := []tc{
		{"meta InvalidCode", 0, 0, 0, 1, 0x00_00_00_01},
		{"kernel buffer",    0, 1, 1, 0, 0x00_01_01_00},
		{"v1 logger",        1, 1, 0, 1, 0x01_01_00_01},
		{"max octets",       255, 255, 255, 255, 0xFF_FF_FF_FF},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := Pack(c.m, c.l, c.p, c.s)
		if got != c.want {
			t.Fatalf("%s: Pack(%d,%d,%d,%d) = %#08x, want %#08x",
				c.name, c.m, c.l, c.p, c.s, uint32(got), uint32(c.want))
		}
		if got.Major() != c.m || got.Layer() != c.l || got.Package() != c.p || got.Serial() != c.s {
			t.Fatalf("%s: accessor round-trip mismatch", c.name)
		}
	}
	for _, c := range tests {
		c := c
		t.Run(c.name, func(t *testing.T) { t.Parallel(); runCase(t, c) })
	}
}

func Test_Comparability_SwitchAndMapKey(t *testing.T) {
	t.Parallel()
	const a Code = 0x00_00_00_01
	const b Code = 0x00_00_00_02
	//: switch on Code compiles and works.
	switch a {
	case a: // expected
	default:
		t.Fatalf("switch did not match identical Code")
	}
	//: map-key usage compiles and works.
	m := map[Code]string{a: "a", b: "b"}
	if m[a] != "a" || m[b] != "b" {
		t.Fatalf("map-key usage failed: %+v", m)
	}
	//: ordering is well-defined (Code is a numeric type).
	if a >= b {
		t.Fatalf("expected a < b, got a=%#x b=%#x", uint32(a), uint32(b))
	}
}

func Test_itoaDecimal(t *testing.T) {
	t.Parallel()
	cases := map[uint8]string{0: "0", 5: "5", 10: "10", 99: "99", 100: "100", 255: "255"}
	for in, want := range cases {
		if got := itoaDecimal(in); got != want {
			t.Errorf("itoaDecimal(%d) = %q, want %q", in, got, want)
		}
	}
}

func Test_itoaPadded3(t *testing.T) {
	t.Parallel()
	cases := map[uint8]string{0: "000", 5: "005", 10: "010", 99: "099", 100: "100", 255: "255"}
	for in, want := range cases {
		if got := itoaPadded3(in); got != want {
			t.Errorf("itoaPadded3(%d) = %q, want %q", in, got, want)
		}
		if len(itoaPadded3(in)) != 3 {
			t.Errorf("itoaPadded3(%d) not length 3: %q", in, itoaPadded3(in))
		}
	}
}
