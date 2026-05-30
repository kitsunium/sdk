package rotfile

import (
	"os"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

func Test_sentinels(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		err  error
		code errs.Code
	}
	tests := []tc{
		{"open sentinel carries its code", RotFileOpenFailed, CodeRotFileOpenFailed},
		{"rotate sentinel carries its code", RotFileRotateFailed, CodeRotFileRotateFailed},
		{"write sentinel carries its code", RotFileWriteFailed, CodeRotFileWriteFailed},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: each defined sentinel must report its own dotted-quad code.
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

func Test_wrapRotate(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"wrapRotate carries the rotate code and wraps the cause"}}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		err := wrapRotate(os.ErrPermission, "service/writer/rotfile.test: private")
		//: the wrapped error must carry the rotate sentinel code.
		if !errs.HasCode(err, CodeRotFileRotateFailed) {
			t.Errorf("wrapRotate err=%v want rotate code", err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
