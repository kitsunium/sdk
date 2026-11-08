//go:build unix && !linux && !illumos && !solaris

package exec

// fdDir lists the process's open descriptors on darwin and the BSDs. On
// FreeBSD it lists 0-2 alone unless fdescfs is mounted, which is why FreeBSD
// seals through close_range first; OpenBSD's static nodes stop at 63.
const fdDir string = "/dev/fd"
