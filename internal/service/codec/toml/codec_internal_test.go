package toml

import "testing"

// Test_tomlCodec_Metadata covers the three metadata methods (Name,
// MIMETypes, Extensions) with a single table-driven suite exercising the
// unexported singleton directly (white-box).
func Test_tomlCodec_Metadata(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		got  string
		want string
	}
	c := &tomlCodec{}
	tests := []tc{
		{"Name returns canonical identifier", c.Name(), "toml"},
		{"MIMETypes first entry is canonical", c.MIMETypes()[0], "application/toml"},
		{"Extensions first entry is canonical", c.Extensions()[0], ".toml"},
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
