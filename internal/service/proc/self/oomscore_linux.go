//go:build linux

package self

import (
	"os"
	"strconv"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// oomScoreAdjPath is the running process's own OOM-killer bias in procfs.
const oomScoreAdjPath string = "/proc/self/oom_score_adj"

// exitOSErr is sysexits.h EX_OSERR (71), the exit status of RLIMIT_FAILED.
const exitOSErr int = 71

// oomScoreAdjPerm is the mode os.WriteFile is given; the procfs file always
// exists, so it is never used to create one.
const oomScoreAdjPerm os.FileMode = 0o644

// setOOMScoreAdj writes n to /proc/self/oom_score_adj; see SetOOMScoreAdj.
func setOOMScoreAdj(n int) error {
	//: the kernel validates the range and the privilege on the write.
	if err := os.WriteFile(oomScoreAdjPath, []byte(strconv.Itoa(n)), oomScoreAdjPerm); err != nil {
		//: EINVAL out of range, EACCES lowering without CAP_SYS_RESOURCE.
		return oomFailed(err, n)
	}
	//: the bias is in force.
	return nil
}

// oomFailed wraps a refused write onto the central RlimitFailed sentinel.
func oomFailed(cause error, n int) error {
	//: restate the RLIMIT_FAILED sentinel's fields without re-Defining the code.
	return errs.Wrap(cause, errs.WrapParams{
		Code:     coreproc.CodeRlimitFailed,
		Reason:   "RLIMIT_FAILED",
		Public:   "Could not apply the resource limit",
		Private:  "service/proc/self.SetOOMScoreAdj: writing /proc/self/oom_score_adj failed",
		ExitCode: exitOSErr,
	}, errs.Int("oom_score_adj", n))
}
