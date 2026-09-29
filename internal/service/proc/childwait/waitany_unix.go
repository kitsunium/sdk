//go:build unix && !solaris

// Package childwait — the pid that means "any child" to wait4 on every Unix but
// illumos and Solaris, whose libc reads it otherwise (waitany_solaris.go).
package childwait

// anyChildPID is the pid argument that makes wait4 collect ANY child: -1, as
// on Linux, darwin and the BSDs.
const anyChildPID int = -1
