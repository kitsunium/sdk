//go:build unix && !(linux && (amd64 || arm64))

package exec

import (
	"os"
	"runtime"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// checkSandbox refuses any Spec.Sandbox before the Path is resolved and before
// any fork: this platform has no second stage to apply it, and a sandbox asked
// for and not applied must never run the program.
func checkSandbox(spec coreproc.Spec) error {
	//: nothing asked.
	if spec.Sandbox == nil {
		return nil
	}
	//: UNSUPPORTED_PLATFORM naming the field and the platform.
	return wrapUnsupportedPlatform(errs.String("feature", "Sandbox"), errs.String("platform", runtime.GOOS+"/"+runtime.GOARCH))
}

// trampolineSelf returns the binary the limits trampoline re-executes.
func trampolineSelf(_ coreproc.Spec) (string, error) {
	//: checkSandbox refused every sandbox, so this is the limits trampoline.
	return os.Executable()
}

// attachSandbox has nothing to attach: checkSandbox refused every sandbox.
func attachSandbox(_ coreproc.Spec, _ *os.ProcAttr) (*os.File, error) {
	//: an ordinary spawn.
	return nil, nil
}

// execSandboxed is never reached here — the parent refused the sandbox — and
// fails closed should a caller set the variable by hand: the target never runs.
func execSandboxed(_, _ string, _, _ []string) {
	reportHandshake(handshakeSandboxFail)
	stderrLine("sdk sandbox: unsupported on " + runtime.GOOS + "/" + runtime.GOARCH)
	os.Exit(trampolineApplyExit)
}
