//go:build unix && !solaris

package childwait

// anyChildPID is the pid argument that makes wait4 collect ANY child: -1, as
// on Linux, darwin and the BSDs.
const anyChildPID int = -1
