package codec_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/core/codec"
)

func TestFormat_String(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   codec.Format
		want string
	}
	tests := []tc{
		{"named value", codec.Format("json"), "json"},
		{"empty", codec.Format(""), ""},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := c.in.String(); got != c.want {
			t.Errorf("%s: String() = %q, want %q", c.name, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestFormat_Known(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   codec.Format
		want bool
	}
	tests := []tc{
		{"empty never known", codec.Format(""), false},
		{"unregistered not known", codec.Format("absent-zzz"), false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := c.in.Known(); got != c.want {
			t.Errorf("%s: Known() = %v, want %v", c.name, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
