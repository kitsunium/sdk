package listen_test

import (
	"errors"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/pkg/v1/proc/systemd/listen"
)

// TestCheckNameThroughTheFacade pins #739's name check on every platform:
// a name LISTEN_FDNAMES cannot carry is the re-exported InvalidFDName.
func TestCheckNameThroughTheFacade(t *testing.T) {
	t.Parallel()
	if err := listen.CheckName("web"); err != nil {
		t.Errorf("CheckName(web) = %v", err)
	}
	if err := listen.CheckName("a:b"); !errors.Is(err, listen.InvalidFDName) || !errs.HasCode(err, listen.CodeInvalidFDName) {
		t.Errorf("CheckName(a:b) = %v, want InvalidFDName", err)
	}
}
