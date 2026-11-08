package cgroup

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// maxSignalPasses bounds Signal's re-reads of cgroup.procs: a tree that
// forks faster than it is signalled, and ignores the signal, cannot hold the
// caller forever.
const maxSignalPasses int = 64

// Signal sends sig to every process of the group and of the groups beneath
// it but the caller's own, reading cgroup.procs again until a pass finds no
// process it has not signalled, at most 64 passes. A process gone before the
// signal reached it is not an error; any other refusal is SignalFailed.
func (g *controlGroup) Signal(sig coreproc.Signal) error {
	//: the subtree's members, kill(2), and this process kept out of reach.
	return signalTree(func() ([]int, error) { return treeMembers(g.dir) },
		func(pid int) error { return syscall.Kill(pid, syscall.Signal(sig.Int())) }, os.Getpid())
}

// signalTree signals, through kill, every pid members lists and has not been
// signalled, until a pass finds none new or maxSignalPasses passes ran. A pid
// of zero or below — a process group to kill(2) — and self are never
// signalled; ESRCH, a process already gone, is no failure.
func signalTree(members func() ([]int, error), kill func(pid int) error, self int) error {
	pass := &signalPass{kill: kill, self: self}
	//: a process forked while a pass signalled is found by the next one.
	for range maxSignalPasses {
		pids, err := members()
		//: the group itself cannot be read: it is gone.
		if err != nil {
			//: CGROUP_READ_FAILED.
			return err
		}
		//: a pass that found nobody new: every process was reached.
		if pass.run(pids) == 0 {
			break
		}
	}
	//: the first refusal, or none.
	return pass.failure
}

// signalPass is what signalTree's passes share: the kill, the caller kept out
// of reach, the pids already signalled, and the first refusal.
type signalPass struct {
	// kill signals one pid.
	kill func(pid int) error
	// self is the caller's own pid, never signalled.
	self int
	// signalled are the pids a pass already reached.
	signalled map[int]bool
	// failure is the first refusal other than ESRCH.
	failure error
}

// run signals each of pids not signalled yet and returns how many it reached.
func (p *signalPass) run(pids []int) int {
	//: sized on the first pass, which lists most of the tree.
	if p.signalled == nil {
		p.signalled = make(map[int]bool, len(pids))
	}
	fresh := 0
	//: each process listed.
	for _, pid := range pids {
		//: one more reached by this pass.
		if p.reach(pid) {
			fresh++
		}
	}
	//: how many this pass reached.
	return fresh
}

// reach signals pid unless it is a process group, the caller, or already
// reached, and reports whether it did; the first refusal other than ESRCH —
// a process already gone — is kept.
func (p *signalPass) reach(pid int) bool {
	//: a process group, the caller itself, or one already reached.
	if pid <= 0 || pid == p.self || p.signalled[pid] {
		return false
	}
	p.signalled[pid] = true
	kerr := p.kill(pid)
	//: the first refusal is kept, a process already gone is none.
	if kerr != nil && !errors.Is(kerr, syscall.ESRCH) && p.failure == nil {
		p.failure = signalFailed(kerr, pid)
	}
	//: signalled, whatever the kernel said.
	return true
}

// Contains reports whether pid is a process of the group or of a group
// beneath it. CgroupReadFailed when the group's cgroup.procs cannot be read.
func (g *controlGroup) Contains(pid int) (bool, error) {
	pids, err := treeMembers(g.dir)
	//: the group cannot be read.
	if err != nil {
		//: CGROUP_READ_FAILED.
		return false, err
	}
	//: a member at any depth.
	return slices.Contains(pids, pid), nil
}

// treeMembers lists the pids of dir's cgroup.procs and of every group beneath
// it. dir's own file must be read; a group beneath that goes during the walk
// is skipped, and a link is never followed.
func treeMembers(dir string) ([]int, error) {
	pids, err := procsOf(dir)
	//: the group itself is gone or unreadable.
	if err != nil {
		//: CGROUP_READ_FAILED, with the directory.
		return nil, cgroupReadFailed(err, dir)
	}
	entries, err := os.ReadDir(dir)
	//: the group went after its procs were read.
	if err != nil {
		//: what was read.
		return pids, nil
	}
	//: each group beneath, depth first.
	for _, entry := range entries {
		//: a file of the group, or a link: neither is a group to walk.
		if !entry.IsDir() {
			continue
		}
		below, berr := treeMembers(filepath.Join(dir, entry.Name()))
		//: a group removed during the walk has no process left.
		if berr != nil {
			continue
		}
		pids = append(pids, below...)
	}
	//: every process of the subtree.
	return pids, nil
}

// procsOf reads the pids of dir's cgroup.procs.
func procsOf(dir string) ([]int, error) {
	content, err := os.ReadFile(filepath.Join(dir, procsFile))
	//: the group is gone.
	if err != nil {
		//: the read's own failure.
		return nil, err
	}
	fields := strings.Fields(string(content))
	pids := make([]int, 0, len(fields))
	//: one pid per line.
	for _, field := range fields {
		//: the kernel writes only decimal pids.
		if pid, perr := strconv.Atoi(field); perr == nil {
			pids = append(pids, pid)
		}
	}
	//: the members, in the kernel's order.
	return pids, nil
}

// signalFailed wraps cause in the central SIGNAL_FAILED sentinel, with the
// pid that refused.
func signalFailed(cause error, pid int) error {
	//: restate the central fields; the code is never re-Defined here.
	return errs.Wrap(cause, errs.WrapParams{
		Code:     coreproc.CodeSignalFailed,
		Reason:   "SIGNAL_FAILED",
		Public:   "Could not deliver the signal",
		Private:  "service/proc: kill(2) failed for the target pid or process group",
		ExitCode: exitOSErr,
	}, errs.Int("pid", pid))
}
