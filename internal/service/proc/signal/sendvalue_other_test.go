//go:build !linux && !freebsd

package signal_test

import (
	"os"
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/proc/signal"
)

// TestSendValueUnsupportedHere pins #942's refusal where no value-carrying
// send is bound: UNSUPPORTED_PLATFORM, and nothing is sent — the target is
// this test's own process, with the null signal.
func TestSendValueUnsupportedHere(t *testing.T) {
	t.Parallel()
	//: refused before any syscall.
	if err := signal.SendValue(signal.Target(os.Getpid()), 0, 1); !errs.HasCode(err, coreproc.CodeUnsupportedPlatform) {
		t.Fatalf("SendValue = %v, want UNSUPPORTED_PLATFORM", err)
	}
}
