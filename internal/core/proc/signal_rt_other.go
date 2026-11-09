//go:build !linux && !freebsd

package proc

// No realtime signal range is named here: darwin, OpenBSD, NetBSD, DragonFly,
// illumos, Solaris and Windows are either without one or without one the SDK
// has verified, so the range is empty and every number in it stays unknown.
const (
	// rtMin is above rtMax: the empty range.
	rtMin Signal = 1
	// rtMax is below rtMin: the empty range.
	rtMax Signal = 0
)
