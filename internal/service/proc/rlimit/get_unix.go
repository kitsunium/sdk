//go:build unix

package rlimit

import (
	"syscall"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/proc/internal/rlim"
)

// syscallGetrlimit names the call a read-back failure wraps.
const syscallGetrlimit string = "getrlimit"

// getLimit reads r for the caller with getrlimit(2); another pid is foreign
// (getForeign: prlimit64 on Linux, refused elsewhere).
func getLimit(pid int, r coreproc.Resource) (coreproc.LimitValue, error) {
	rl, err := resolve(r)
	//: an unmapped resource is a usage error, not a syscall failure.
	if err != nil {
		//: UNKNOWN_RESOURCE verbatim.
		return coreproc.LimitValue{}, err
	}
	//: another process.
	if pid != 0 && pid != syscall.Getpid() {
		//: the platform's foreign read.
		return getForeign(pid, rl, r)
	}
	var limit syscall.Rlimit
	//: getrlimit(2) reads the calling process.
	if gerr := syscall.Getrlimit(rl, &limit); gerr != nil {
		//: the shared RLIMIT_FAILED wrapper.
		return coreproc.LimitValue{}, readFailed(gerr, pid, r.String(), syscallGetrlimit)
	}
	soft, hard := rlim.Pair(limit)
	//: the ceiling in force.
	return coreproc.LimitValue{Soft: soft, Hard: hard}, nil
}

// readFailed wraps a failed read-back in the central RLIMIT_FAILED sentinel.
func readFailed(cause error, pid int, resource, call string) error {
	//: restate the central RLIMIT_FAILED fields; never re-Define the code.
	return errs.Wrap(cause, errs.WrapParams{
		Code:     coreproc.CodeRlimitFailed,
		Reason:   "RLIMIT_FAILED",
		Public:   "Could not read the resource limit",
		Private:  "service/proc/rlimit.Get: reading the limit back failed",
		ExitCode: exitOSErr,
	}, errs.Int("pid", pid), errs.String("resource", resource), errs.String("syscall", call))
}
