//go:build unix && !darwin

package exec

// rssUnitsPerKiB is 1 because Linux, FreeBSD, OpenBSD, NetBSD and DragonFly
// report ru_maxrss in kilobytes, the unit the port documents. illumos and
// Solaris define the field in pages but leave it 0, so the figure there is 0
// whatever the unit.
const rssUnitsPerKiB int64 = 1
