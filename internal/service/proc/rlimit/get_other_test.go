//go:build !unix

package rlimit_test

import (
	"runtime"
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/proc/rlimit"
)

// TestGetOffUnix pins #939's rows off Unix: Get is UNSUPPORTED_PLATFORM, and
// OpenFiles counts handles on Windows.
func TestGetOffUnix(t *testing.T) {
	t.Parallel()
	if _, err := rlimit.Get(0, coreproc.ResourceNoFile); !errs.HasCode(err, coreproc.CodeUnsupportedPlatform) {
		t.Fatalf("Get = %v, want UNSUPPORTED_PLATFORM", err)
	}
	n, err := rlimit.OpenFiles()
	if runtime.GOOS == "windows" && (err != nil || n < 1) {
		t.Fatalf("OpenFiles on windows = %d, %v; want a handle count", n, err)
	}
}
