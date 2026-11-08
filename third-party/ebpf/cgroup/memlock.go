package cgroup

import (
	"strconv"
	"strings"
	"sync"

	"github.com/kitsunium/sdk/internal/kernel/errs"

	corebpf "github.com/kitsunium/sdk/internal/core/proc/bpf"
)

// The first kernel that charges BPF memory to the memory cgroup instead of
// RLIMIT_MEMLOCK (commit 97306be45fbe, Linux 5.11).
const (
	memcgMajor int = 5
	memcgMinor int = 11
)

// memlocker raises RLIMIT_MEMLOCK once, before the first load, on a kernel
// that still charges BPF memory to it. cilium/ebpf's rlimit package does the
// same with a probe, but that probe runs in its init: it lowers the limit to
// zero and calls bpf() in every program that links this module, at start,
// whether or not it ever loads a program. This one runs at the first load,
// and only before 5.11.
type memlocker struct {
	once sync.Once
	err  error
	// release returns the running kernel's release, uname -r.
	release func() (string, error)
	// raise lifts RLIMIT_MEMLOCK to infinity.
	raise func() error
}

// remove raises the limit when the kernel needs it, once per process; the
// answer of the first call is every call's. A failure is LOAD_REFUSED: no
// program could be loaded past it.
func (m *memlocker) remove() error {
	//: one decision per process.
	m.once.Do(func() {
		release, err := m.release()
		//: a kernel that does not say its release is assumed to need it.
		if err == nil && !needsMemlock(release) {
			//: 5.11 or later: the memory cgroup pays.
			return
		}
		//: before 5.11, or unknown: lift the limit.
		if raiseErr := m.raise(); raiseErr != nil {
			//: CAP_SYS_RESOURCE is missing.
			m.err = errs.Wrap(raiseErr, corebpf.LoadRefused.Params(), errs.String("step", "memlock"), errs.String("release", release))
		}
	})
	//: the first call's answer.
	return m.err
}

// needsMemlock reports whether a kernel of this release charges BPF memory
// to RLIMIT_MEMLOCK: before 5.11, or when the release cannot be read.
func needsMemlock(release string) bool {
	major, minor, ok := kernelVersion(release)
	//: an unreadable release: raising is harmless, failing a load is not.
	if !ok {
		//: assume an old kernel.
		return true
	}
	//: before 5.11.
	return major < memcgMajor || (major == memcgMajor && minor < memcgMinor)
}

// kernelVersion reads the major and minor numbers of a kernel release —
// "6.12.101+deb13-amd64", "5.10.0-28-cloud-amd64", "4.18.0-553.el8_10".
func kernelVersion(release string) (major, minor int, ok bool) {
	fields := strings.SplitN(release, ".", 3)
	//: at least "major.minor".
	if len(fields) < 2 {
		//: not a release.
		return 0, 0, false
	}
	major, errMajor := strconv.Atoi(fields[0])
	minor, errMinor := strconv.Atoi(leadingDigits(fields[1]))
	//: both numbers, or no version.
	if errMajor != nil || errMinor != nil {
		//: not a release.
		return 0, 0, false
	}
	//: the two numbers.
	return major, minor, true
}

// leadingDigits is the decimal digits s starts with: "0-28-cloud" gives "0".
func leadingDigits(s string) string {
	end := strings.IndexFunc(s, func(r rune) bool { return r < '0' || r > '9' })
	//: all digits.
	if end < 0 {
		//: the whole string.
		return s
	}
	//: up to the first non-digit.
	return s[:end]
}
