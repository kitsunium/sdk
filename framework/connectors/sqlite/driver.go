//go:build !(dragonfly || illumos || solaris)

// Package sqlite — the driver, linked on every GOOS modernc.org/sqlite has a
// port to.
package sqlite

import _ "modernc.org/sqlite" // the driver, registered as "sqlite"

// driverLinked is true where modernc.org/sqlite has a port: the driver is
// registered as "sqlite".
const driverLinked = true
