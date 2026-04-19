package msgpack

import "testing"

// Test_msgpackCodec_Metadata covers the three metadata methods (Name,
// MIMETypes, Extensions) with a single table-driven suite exercising the
// unexported singleton directly (white-box).
func Test_msgpackCodec_Metadata(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		got  string
		want string
	}
	c := &msgpackCodec{}
	tests := []tc{
		{"Name returns canonical identifier", c.Name(), "msgpack"},
		{"MIMETypes first entry is canonical", c.MIMETypes()[0], "application/msgpack"},
		{"Extensions first entry is canonical", c.Extensions()[0], ".msgpack"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		if tc.got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, tc.got, tc.want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}
