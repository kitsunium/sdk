//go:build dragonfly || illumos || solaris

// Package sqlite — no driver on dragonfly, illumos and solaris, which
// modernc.org/sqlite has no port to.
package sqlite

// driverLinked is false where modernc.org/sqlite has no port (its libc
// excludes these GOOS values): the module still builds, and Open refuses.
const driverLinked = false
