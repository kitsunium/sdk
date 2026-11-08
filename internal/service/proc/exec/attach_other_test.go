//go:build !(linux && !mips && !mipsle && !mips64 && !mips64le) && !darwin && !dragonfly && !freebsd && !netbsd && !openbsd

package exec_test

import (
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcexec "github.com/kitsunium/sdk/internal/service/proc/exec"
)

// TestAttachUnsupported pins #729's refusal off Linux: no adoption, typed,
// before any file is read.
func TestAttachUnsupported(t *testing.T) {
	t.Parallel()
	if p, err := svcexec.Attach(t.Context(), 4242); p != nil || !errs.HasCode(err, coreproc.CodeUnsupportedPlatform) {
		t.Errorf("Attach = %v, %v, want UNSUPPORTED_PLATFORM", p, err)
	}
	p, err := svcexec.AttachPIDFile(t.Context(), "/nonexistent.pid", coreproc.AttachValue{})
	if p != nil || !errs.HasCode(err, coreproc.CodeUnsupportedPlatform) {
		t.Errorf("AttachPIDFile = %v, %v, want UNSUPPORTED_PLATFORM", p, err)
	}
}
