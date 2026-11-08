//go:build unix

package exec

import (
	"strconv"
	"syscall"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// trampolineSandboxEnv carries the descriptor number of the sealed memfd that
// holds a sandboxed spawn's payload (Spec.Sandbox): it sits right after the
// handshake pipe in the child's table. Stripped before the target runs.
const trampolineSandboxEnv string = "__KITSUNIUM_SDK_PROC_SANDBOX"

// handshakeSandboxFail opens a sandbox failure report: the step's number and
// the errno's two low-order bytes follow it.
const handshakeSandboxFail byte = 'S'

// sandboxReportLen is the length of a sandbox failure report: the tag, the
// step and the errno.
const sandboxReportLen int = 4

// The steps of the sandbox's second stage, in the order the kernel imposes on
// them: a step may need a privilege a later one gives up, Landlock and seccomp
// need no_new_privs, and the seccomp filter, installed last, must not refuse
// what the stage still does before execve. Step 0 is the reading of the
// payload; the cgroup namespace comes first of all, the child already in its
// group whichever placed it — the clone or the trampoline's write —, so the
// namespace is rooted at that group. The numbers travel in a failure report
// and in the tests' trace.
const (
	stepPayload uint8 = iota
	stepCgroupNamespace
	stepMounts
	stepRoot
	stepLandlockPaths
	stepBounding
	stepSecureBits
	stepIdentity
	stepCapabilities
	stepFDSweep
	stepNoNewPrivs
	stepLandlock
	stepSeccomp
)

// sandboxStepNames names each step in a SandboxFailed error's step field,
// indexed by its number.
var sandboxStepNames = [...]string{
	stepPayload:         "payload",
	stepCgroupNamespace: "cgroup_namespace",
	stepMounts:          "mounts",
	stepRoot:            "pivot_root",
	stepLandlockPaths:   "landlock_paths",
	stepBounding:        "capbset_drop",
	stepSecureBits:      "securebits",
	stepIdentity:        "identity",
	stepCapabilities:    "capabilities",
	stepFDSweep:         "cloexec_sweep",
	stepNoNewPrivs:      "no_new_privs",
	stepLandlock:        "landlock_restrict_self",
	stepSeccomp:         "seccomp",
}

// sandboxStepName returns the name of step, or its number when it is none.
func sandboxStepName(step uint8) string {
	//: a step this build knows.
	if int(step) < len(sandboxStepNames) {
		return sandboxStepNames[step]
	}
	//: a number from nowhere: said as it is.
	return "step " + strconv.Itoa(int(step))
}

// sandboxReport is the failure report of step with errno, as the stage writes
// it to the handshake pipe.
func sandboxReport(step uint8, errno syscall.Errno) [sandboxReportLen]byte {
	//: the tag, the step, the errno's low and high byte.
	return [sandboxReportLen]byte{handshakeSandboxFail, step, byte(errno), byte(errno >> 8)}
}

// sandboxFailure maps a sandbox failure report — what follows the tag — to the
// central SandboxFailed, naming the step and the errno.
func sandboxFailure(rest []byte) error {
	//: a report cut short still says the sandbox failed.
	if len(rest) < sandboxReportLen-1 {
		return wrapSandboxFailed(nil, errs.String("step", "unknown"))
	}
	errno := syscall.Errno(uint16(rest[1]) | uint16(rest[2])<<8)
	//: the step and the kernel's answer.
	return wrapSandboxFailed(errno, errs.String("step", sandboxStepName(rest[0])), errs.String("errno", errno.Error()))
}
