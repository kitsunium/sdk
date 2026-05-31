package transform_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/core/transform"
)

func TestAlgorithm_String(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   transform.Algorithm
		want string
	}
	tests := []tc{
		{"named algorithm", "gzip", "gzip"},
		{"empty zero value", "", ""},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: String is a plain cast back to the raw identifier.
		if got := c.in.String(); got != c.want {
			t.Errorf("%s: String()=%q want %q", c.name, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestAlgorithm_Known(t *testing.T) {
	//: sequential — seeds + reads the process-wide registry.
	transform.ResetForTest()
	transform.Register(&fakeCompressor{name: "known-fake"})
	type tc struct {
		name   string
		in     transform.Algorithm
		wantOK bool
	}
	tests := []tc{
		{"registered algorithm is known", "known-fake", true},
		{"unregistered algorithm is unknown", "absent-zzz", false},
		{"empty zero value is never known", "", false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: Known short-circuits the empty zero value and delegates otherwise.
		if got := c.in.Known(); got != c.wantOK {
			t.Errorf("%s: Known()=%v want %v", c.name, got, c.wantOK)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}
