//go:build linux && (amd64 || arm64 || riscv64 || loong64)

package loop

import (
	"syscall"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// The setup flags every ring may carry: the floor every kernel the backend
// serves accepts, and SINGLE_ISSUER, which 6.0 added.
const (
	// setupBase is what a ring carries whatever the probe finds.
	setupBase uint32 = setupCoopTaskrun | setupSubmitAll
	// rewindFirst and rewindSecond are the user_data of SQ_REWIND's two
	// self-test NOPs: the order they complete in is the contract.
	rewindFirst  uint64 = 0xA1
	rewindSecond uint64 = 0xA2
)

// setupDrops is the order in which the probe gives up setup flags on EINVAL,
// the newest first, so a flag is never dropped before one that requires it:
// SQ_REWIND before NO_SQARRAY (REWIND⇒NO_SQARRAY), DEFER_TASKRUN before
// SINGLE_ISSUER (DEFER⇒SINGLE).
var setupDrops = [...]struct {
	flag uint32
	name corenet.Capability
}{
	{setupSQRewind, corenet.CapabilityURingSQRewind},
	{setupNoSQArray, corenet.CapabilityURingNoSQArray},
	{setupDeferTaskrun, corenet.CapabilityURingDeferTaskrun},
	{setupSingleIssuer, ""},
}

// probe runs stages 1 to 3 on throwaway rings, on this locked thread from
// setup to close (ADR 0189): io_uring_setup with every flag the switches
// allow, giving them up one by one on EINVAL (setupDrops); the features;
// REGISTER_PROBE for every op this backend issues; RING_FDS registered and
// unregistered before the close; SQ_REWIND's partial submit. It settles the
// primary's and the secondaries' flags and fills the report.
func (ul *uLoop) probe() error {
	//: stage 1, injected.
	if ul.hooks.failStage == "setup" {
		//: injected.
		return probeError("setup", syscall.EPERM)
	}
	probed, found, err := ul.openDescending()
	//: no ring at all.
	if err != nil {
		//: named by the step.
		return err
	}
	defer probed.close()
	ul.stage = "setup"
	ok, perr := probed.supports(opAccept, opRecv, opSend, opAsyncCancel, opClose, opPollAdd, opRead, opNop, opShutdown)
	//: stage 3: the ops this backend issues.
	if perr != 0 || !ok || ul.hooks.failStage == "probe" {
		//: named.
		return probeError("register_probe", orErrno(perr, syscall.EOPNOTSUPP))
	}
	ul.stage = "register_probe"
	ul.probeRingFDs(probed)
	rewind := found&setupSQRewind != 0 && ul.rewindSelfTest(probed)
	ul.settle(found, rewind, probed.params.features)
	//: the flags every ring is built with.
	return nil
}

// openDescending opens the probe's ring with every setup flag the switches
// allow and gives them up in setupDrops' order while the kernel answers
// EINVAL, recording each one given up as absent. It returns the ring and the
// flags it was opened with.
func (ul *uLoop) openDescending() (*uring, uint32, error) {
	flags := ul.probeFlags()
	opened, step, errno := openURing(probeEntries, flags)
	//: the newest flag first, while the kernel does not know it.
	for _, d := range setupDrops {
		//: opened, or refused for another reason than a flag.
		if errno != syscall.EINVAL || step != "setup" {
			break
		}
		//: not asked for: nothing to give up.
		if flags&d.flag == 0 {
			continue
		}
		flags = ul.giveUp(flags, d.flag, d.name, errno)
		opened, step, errno = openURing(probeEntries, flags)
	}
	//: no ring at all.
	if errno != 0 {
		//: named by the step.
		return nil, 0, probeError(step, errno)
	}
	//: opened.
	return opened, flags, nil
}

// probeFlags is the first setup the probe tries: every class A-code flag the
// operator left on, SQ_REWIND only with NO_SQARRAY, which it requires, and
// DEFER_TASKRUN in place of COOP_TASKRUN.
func (ul *uLoop) probeFlags() uint32 {
	flags := setupSingleIssuer | setupSubmitAll | setupCoopTaskrun
	//: each class A-code flag, unless the operator switched it off.
	for _, d := range setupDrops[:3] {
		//: the operator's switch: never probed.
		if !ul.sw.disabled(d.name) {
			flags |= d.flag
		}
	}
	//: REWIND⇒NO_SQARRAY: without the second the first is not tried.
	if flags&setupNoSQArray == 0 && flags&setupSQRewind != 0 {
		flags &^= setupSQRewind
		ul.rep.set(corenet.CapabilityURingSQRewind, statusAbsent, "needs uring-no-sqarray, switched off")
	}
	//: DEFER_TASKRUN replaces COOP_TASKRUN on a ring.
	if flags&setupDeferTaskrun != 0 {
		flags &^= setupCoopTaskrun
	}
	//: the first try.
	return flags
}

// giveUp drops flag from flags after the kernel refused it, reporting the
// capability it names absent; DEFER_TASKRUN given up makes the ring
// cooperative again.
func (ul *uLoop) giveUp(flags, flag uint32, name corenet.Capability, errno syscall.Errno) uint32 {
	flags &^= flag
	//: cooperative again.
	if flag == setupDeferTaskrun {
		flags |= setupCoopTaskrun
	}
	//: a capability the kernel refused at setup.
	if name != "" {
		ul.rep.set(name, statusAbsent, probeReason("io_uring_setup", errno))
	}
	//: the next try.
	return flags
}

// probeRingFDs registers the probe's ring with this thread and unregisters
// it before the ring closes: a ring left registered outlives its close.
func (ul *uLoop) probeRingFDs(probed *uring) {
	//: the operator's switch: never probed.
	if ul.sw.disabled(corenet.CapabilityURingRingFDs) {
		//: disabled.
		return
	}
	errno := probed.registerRing()
	//: refused.
	if errno != 0 {
		ul.rep.set(corenet.CapabilityURingRingFDs, statusAbsent, probeReason("register_ring_fds", errno))
		return
	}
	probed.unregisterRing()
	ul.rep.found(ul.sw, corenet.CapabilityURingRingFDs, true, "")
	ul.uses.ringFDs = ul.sw.wants(corenet.CapabilityURingRingFDs)
}

// rewindSelfTest checks SQ_REWIND's contract on the probe's ring: two NOPs
// filled from SQE 0, one submitted, then the one left moved to SQE 0 and
// submitted, each completion carrying its own user_data. A kernel that read
// the head instead of SQE 0 would complete the first twice.
func (ul *uLoop) rewindSelfTest(probed *uring) bool {
	for _, ud := range []uint64{rewindFirst, rewindSecond} {
		s := probed.get()
		//: no room on a ring of 16.
		if s == nil {
			ul.rep.set(corenet.CapabilityURingSQRewind, statusAbsent, "partial submit: no SQE")
			//: failed.
			return false
		}
		s.opcode, s.userData = opNop, ud
	}
	fd, reg := probed.enterTarget()
	got, errno := uringEnterFlags(fd, 1, reg)
	//: exactly one taken.
	if errno != 0 || got != 1 {
		ul.rep.set(corenet.CapabilityURingSQRewind, statusAbsent, probeReason("partial submit", orErrno(errno, syscall.EIO)))
		//: failed.
		return false
	}
	probed.consumed(1)
	probed.submit()
	var seen []uint64
	deadline := time.Now().Add(500 * time.Millisecond)
	tick := kernelTimespec{nsec: int64(tickEvery)}
	//: until both came back, or out of time.
	for len(seen) < 2 && time.Now().Before(deadline) {
		probed.wait(&tick, 1)
		probed.reap(8, func(c *cqe) { seen = append(seen, c.userData) })
	}
	//: in order, each once.
	if len(seen) != 2 || seen[0] != rewindFirst || seen[1] != rewindSecond {
		ul.rep.set(corenet.CapabilityURingSQRewind, statusAbsent, "partial submit: completions out of contract")
		//: failed.
		return false
	}
	//: the contract holds.
	return true
}

// settle derives the serving flags from what setup accepted and reports the
// setup capabilities. found is the probe ring's flags, rewind whether
// SQ_REWIND passed its self-test, features the kernel's.
func (ul *uLoop) settle(found uint32, rewind bool, features uint32) {
	sw := ul.sw
	base := setupBase | found&setupSingleIssuer
	//: present only: an absent flag's row already holds giveUp's reason.
	for _, d := range setupDrops[1:3] {
		//: accepted at setup.
		if found&d.flag != 0 {
			ul.rep.found(sw, d.name, true, "")
		}
	}
	//: the self-test failed: its row already says why.
	if rewind {
		ul.rep.found(sw, corenet.CapabilityURingSQRewind, true, "")
	}
	base |= layoutFlags(sw, found, rewind)
	ul.primary, ul.secondary = base, base
	//: DEFER_TASKRUN on the ring the thread waits on, never on the others.
	if found&setupDeferTaskrun != 0 && sw.wants(corenet.CapabilityURingDeferTaskrun) {
		ul.primary = base&^setupCoopTaskrun | setupDeferTaskrun
	}
	ul.uses.multishot = sw.wants(corenet.CapabilityURingMultishotRecv)
	ul.reportMinTimeout(features)
}

// layoutFlags is the SQ layout every ring takes: NO_SQARRAY as asked, or
// because SQ_REWIND needs it, and SQ_REWIND once its self-test passed.
func layoutFlags(sw capSwitches, found uint32, rewind bool) uint32 {
	var flags uint32
	useRewind := rewind && sw.wants(corenet.CapabilityURingSQRewind)
	//: NO_SQARRAY as asked, or because SQ_REWIND needs it.
	if found&setupNoSQArray != 0 && (useRewind || sw.wants(corenet.CapabilityURingNoSQArray)) {
		flags |= setupNoSQArray
	}
	//: SQ_REWIND, tested.
	if useRewind {
		flags |= setupSQRewind
	}
	//: the layout.
	return flags
}

// reportMinTimeout is class B's MIN_TIMEOUT row: what the features say,
// never turned on (minWaitUsec stays zero). The grouped wait bounds its
// timeout instead (ADR 0199 §D3): what min_wait_usec does once its window
// passed empty differs across the kernels that advertise it.
func (ul *uLoop) reportMinTimeout(features uint32) {
	status, reason := statusAbsent, "IORING_FEAT_MIN_TIMEOUT not advertised; "+reasonClassB
	//: advertised.
	if features&featMinTimeout != 0 {
		status, reason = statusAvailable, reasonClassB
	}
	//: a switched-off row keeps its own.
	ul.rep.set(corenet.CapabilityURingMinTimeout, status, reason)
}

// reportSelfTest records what the self-test proved: multishot recv and the
// buffer ring served the payload, and the release check chose the mode.
func (ul *uLoop) reportSelfTest(direct bool) {
	ul.rep.found(ul.sw, corenet.CapabilityURingPbuf, true, "")
	ul.rep.found(ul.sw, corenet.CapabilityURingMultishotRecv, true, "")
	//: the operator's switch: the release check never ran.
	if ul.sw.disabled(corenet.CapabilityURingDirect) {
		//: disabled.
		return
	}
	reason := ""
	//: the loop's close did not tell the peer: process descriptors.
	if !direct {
		reason = "release check: the peer saw no end within 200 ms"
	}
	ul.rep.found(ul.sw, corenet.CapabilityURingDirect, direct, reason)
}

// registerRing registers a serving ring with this thread when RING_FDS is
// on, at most ringFDsMax a thread: a ring past them is entered by its
// descriptor, which the report says.
func (ul *uLoop) registerRing(u *uring) {
	//: off, the thread's table full, or refused: entered by descriptor.
	if !ul.uses.ringFDs || ul.registered >= ringFDsMax || u.registerRing() != 0 {
		//: as is.
		return
	}
	ul.registered++
}

// probeReason names a probe step's refusal. EEXIST is a defect of the probe,
// never of the kernel: a SINGLE_ISSUER ring entered or registered off the
// thread that set it up (ADR 0189), which a probe run under LockOSThread from
// setup to close never does.
func probeReason(step string, errno syscall.Errno) string {
	//: the probe's own fault.
	if errno == syscall.EEXIST {
		//: named as such.
		return "probe defect: " + step + ": " + errno.Error()
	}
	//: the kernel's answer.
	return step + ": " + errno.Error()
}
