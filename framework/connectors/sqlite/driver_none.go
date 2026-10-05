//go:build dragonfly || illumos || solaris

package sqlite

// driverLinked is false where modernc.org/sqlite has no port (its libc
// excludes these GOOS values): the module still builds, and Open refuses.
const driverLinked = false
