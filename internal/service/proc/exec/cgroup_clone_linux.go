//go:build linux

package exec

import (
	"errors"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// cloneIntoCgroupMajor and cloneIntoCgroupMinor are the first Linux release
// with clone3's CLONE_INTO_CGROUP (5.7), which SysProcAttr.UseCgroupFD uses.
const (
	cloneIntoCgroupMajor int = 5
	cloneIntoCgroupMinor int = 7
)

var (
	// cloneIntoCgroupProbe reports, once per process, whether this kernel can
	// clone a child straight into a cgroup — a variable so a test can answer
	// for a kernel it does not run on.
	cloneIntoCgroupProbe = sync.OnceValue(kernelClonesIntoCgroup)

	// cloneIntoCgroupRefused records that a clone into a group answered ENOSYS
	// — a seccomp filter that blocks clone3 on a new kernel — so every later
	// spawn places its child through the trampoline.
	cloneIntoCgroupRefused atomic.Bool
)

// kernelClonesIntoCgroup reads the running kernel's release from uname(2).
func kernelClonesIntoCgroup() bool {
	var u syscall.Utsname
	//: no uname, no claim.
	if err := syscall.Uname(&u); err != nil {
		//: the trampoline is right on every kernel.
		return false
	}
	release := make([]byte, 0, len(u.Release))
	//: Utsname's fields are NUL-terminated int8 (or uint8) arrays.
	for _, c := range u.Release {
		//: the release ends at its NUL.
		if c == 0 {
			break
		}
		release = append(release, byte(c))
	}
	//: "6.12.101+deb13-amd64" and the like.
	return releaseClonesIntoCgroup(string(release))
}

// releaseClonesIntoCgroup reports whether a uname release names Linux 5.7 or
// later. A release it cannot read is not trusted with the placement.
func releaseClonesIntoCgroup(release string) bool {
	majorStr, minorStr, ok := strings.Cut(release, ".")
	//: the minor number ends at the next dot or the first non-digit.
	if i := strings.IndexFunc(minorStr, func(r rune) bool { return r < '0' || r > '9' }); i >= 0 {
		minorStr = minorStr[:i]
	}
	major, e1 := strconv.Atoi(majorStr)
	minor, e2 := strconv.Atoi(minorStr)
	//: an unreadable release.
	if !ok || e1 != nil || e2 != nil {
		//: the trampoline stays in charge.
		return false
	}
	//: 5.7 or later.
	return major > cloneIntoCgroupMajor || (major == cloneIntoCgroupMajor && minor >= cloneIntoCgroupMinor)
}

// cgroupByClone reports whether a placement in cgroupPath is made by the clone
// itself (clone3 with CLONE_INTO_CGROUP) rather than by the trampoline.
func cgroupByClone(cgroupPath string) bool {
	//: a placement, on a kernel that clones into a group, not refused since.
	return cgroupPath != "" && !cloneIntoCgroupRefused.Load() && cloneIntoCgroupProbe()
}

// attachCgroupFD opens spec's validated cgroup directory and asks the fork to
// clone the child into it (#929): the child is a member from its first
// instruction, with no re-exec and no handshake. The returned release closes
// the directory once the fork returned.
func attachCgroupFD(spec coreproc.Spec, attr *syscall.SysProcAttr) (release func(), err error) {
	//: the trampoline places the child, or there is nothing to place.
	if !cgroupByClone(spec.CgroupPath) {
		//: nothing opened.
		return func() {}, nil
	}
	fd, oErr := syscall.Open(spec.CgroupPath, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	//: the directory went away since validateCgroupPath.
	if oErr != nil {
		//: the typed CGROUP_UNAVAILABLE.
		return nil, wrapCgroupUnavailable(oErr, errs.String("cgroup_path", spec.CgroupPath))
	}
	attr.UseCgroupFD, attr.CgroupFD = true, fd
	//: the fork holds its own reference; the parent's goes after it.
	return func() { swallowErr(syscall.Close(fd)) }, nil
}

// cloneSpawnError reads the failure of a fork that cloned into a group:
// ENOSYS — clone3 filtered — is retried once through the trampoline (retry,
// and the refusal is remembered in refused); a refusal by the group itself (EACCES or
// EPERM when it is not delegated, EBUSY for a domain group with controllers,
// EOPNOTSUPP for a threaded one) is CGROUP_WRITE_FAILED, as the trampoline's
// refused cgroup.procs write was; anything else is SPAWN_FAILED.
func cloneSpawnError(cause error, cgroupPath, path string, refused *atomic.Bool) (retry bool, err error) {
	//: clone3 is unavailable however new the kernel says it is.
	if errors.Is(cause, syscall.ENOSYS) {
		refused.Store(true)
		//: the trampoline places the child instead.
		return true, nil
	}
	//: the group refused the member.
	if errors.Is(cause, syscall.EACCES) || errors.Is(cause, syscall.EPERM) ||
		errors.Is(cause, syscall.EBUSY) || errors.Is(cause, syscall.EOPNOTSUPP) {
		//: the central CGROUP_WRITE_FAILED.
		return false, errs.Wrap(cause, errs.WrapParams{
			Code:     coreproc.CodeCgroupWriteFailed,
			Reason:   "CGROUP_WRITE_FAILED",
			Public:   "Could not write the cgroup controller file",
			Private:  "service/proc/exec: the kernel refused to clone the child into the cgroup",
			ExitCode: exitOSErr,
		}, errs.String("cgroup_path", cgroupPath))
	}
	//: an ordinary fork/exec failure.
	return false, wrapSpawn(cause, errs.String("path", path))
}

// cgroupByTrampoline reports whether a placement in cgroupPath needs the
// trampoline's pre-exec cgroup.procs write: a placement the clone cannot make.
func cgroupByTrampoline(cgroupPath string) bool {
	//: a placement, and no clone to make it.
	return cgroupPath != "" && !cgroupByClone(cgroupPath)
}
