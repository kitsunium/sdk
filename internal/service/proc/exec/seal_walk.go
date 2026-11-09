//go:build unix && !((linux && !mips && !mipsle && !mips64 && !mips64le) || freebsd)

package exec

// sealFDs walks the descriptor directory: darwin, OpenBSD, NetBSD, DragonFly,
// illumos, Solaris and Linux on mips have no close_range with
// CLOSE_RANGE_CLOEXEC this package reaches.
func sealFDs() error {
	//: mark what the directory lists.
	return sealByWalk(fdDir)
}
