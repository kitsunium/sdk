package rotfile

import (
	"os"
	"testing"

	corerotfile "github.com/kitsunium/sdk/internal/core/observe/logger/writer/rotfile"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

func Test_wrapRotate(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"wrapRotate carries the rotate code and wraps the cause"}}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		err := wrapRotate(os.ErrPermission, "service/observe/logger/writer/rotfile.test: private")
		//: the wrapped error must carry the rotate sentinel code.
		if !errs.HasCode(err, corerotfile.CodeRotFileRotateFailed) {
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
