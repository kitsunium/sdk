//go:build linux || freebsd

package exec

import (
	"syscall"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// checkDieWithParent accepts any DieWithParent: Linux and FreeBSD carry the
// parent-death signal in SysProcAttr.Pdeathsig.
func checkDieWithParent(_ coreproc.Signal) error {
	//: the kernel honours every signal it can deliver.
	return nil
}

// setDieWithParent asks the forked child to receive sig when its parent
// thread exits (PR_SET_PDEATHSIG on Linux, procctl(PROC_PDEATHSIG_CTL) on
// FreeBSD). The stdlib sets it after the credential change and, on Linux,
// signals the child at once if the parent died before it could, so no window
// is left open. A zero sig leaves the field unset.
func setDieWithParent(attr *syscall.SysProcAttr, sig coreproc.Signal) {
	//: the zero Signal requests nothing.
	if sig == 0 {
		//: no parent-death signal.
		return
	}
	//: the child's parent-death signal, set between fork and exec.
	attr.Pdeathsig = syscall.Signal(sig.Int())
}
