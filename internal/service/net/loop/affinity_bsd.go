//go:build darwin || freebsd || openbsd || netbsd || dragonfly

package loop

import (
	"runtime"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// pin records that a loop's thread is not pinned on kqueue: package syscall
// has no thread affinity call on darwin and the BSDs (ADR 0222). It runs on
// the loop's thread, after LockOSThread, as Linux's does.
func (lp *loop) pin() {
	//: nothing asked.
	if lp.cpu < 0 {
		//: unpinned.
		return
	}
	//: the shared sentinel, wrapped, never redeclared (ADR 0018 §(a)).
	lp.pinErr = errs.Wrap(coreproc.UnsupportedPlatform, errs.WrapParams{},
		errs.String("capability", string(corenet.CapabilityLoopAffinity)), errs.String("goos", runtime.GOOS))
}

// reportAffinity records the loop-affinity row once every loop reported
// ready: missing — a fault, the group asked — when Affinity names a CPU, and
// unprobed when it names none.
func reportAffinity(rep *capReport, loops []*loop) {
	//: each loop, in order.
	for _, lp := range loops {
		//: asked, and refused.
		if lp.cpu >= 0 && lp.pinErr != nil {
			rep.set(corenet.CapabilityLoopAffinity, statusMissing, lp.pinErr.Error())
			//: missing.
			return
		}
	}
	rep.set(corenet.CapabilityLoopAffinity, statusUnprobed, "no Affinity")
}
