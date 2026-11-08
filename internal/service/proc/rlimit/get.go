package rlimit

import coreproc "github.com/kitsunium/sdk/internal/core/proc"

// Get reads the soft/hard ceiling of resource r for pid, 0 being the caller:
// getrlimit(2) for the caller, prlimit64(2) with no new value for another
// pid on Linux, and UnsupportedPlatform for another pid elsewhere — Apply's
// rule — and off Unix. An unmapped resource is UnknownResource; a refused
// read RlimitFailed wrapping the errno.
func Get(pid int, r coreproc.Resource) (coreproc.LimitValue, error) {
	//: delegate to the build-tagged implementation.
	return getLimit(pid, r)
}

// OpenFiles counts the caller's open descriptors, to alert against Get's
// RLIMIT_NOFILE: /proc/self/fd on Linux, illumos and Solaris, /dev/fd on
// darwin, sysctl kern.proc.nfds on FreeBSD (13 and later), and on Windows the
// process's HANDLE count — files, but also events, threads and keys. OpenBSD,
// NetBSD and DragonFly answer UnsupportedPlatform: their /dev/fd nodes are
// static.
func OpenFiles() (int, error) {
	//: delegate to the build-tagged implementation.
	return openFiles()
}
