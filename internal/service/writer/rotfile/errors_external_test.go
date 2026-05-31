package rotfile_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/writer/rotfile"
)

func TestRotFileSentinelsExported(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		err  error
		code errs.Code
	}
	tests := []tc{
		{"open sentinel is introspectable", rotfile.RotFileOpenFailed, rotfile.CodeRotFileOpenFailed},
		{"rotate sentinel is introspectable", rotfile.RotFileRotateFailed, rotfile.CodeRotFileRotateFailed},
		{"write sentinel is introspectable", rotfile.RotFileWriteFailed, rotfile.CodeRotFileWriteFailed},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: each exported sentinel must report its own dotted-quad code so
		//: consumers can route on it via the public errs accessors.
		if !errs.HasCode(c.err, c.code) {
			t.Errorf("%s: err=%v missing code %#x", c.name, c.err, uint32(c.code))
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
