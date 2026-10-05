//go:build !(dragonfly || illumos || solaris)

package sqlite

import _ "modernc.org/sqlite" // the driver, registered as "sqlite"

// driverLinked is true where modernc.org/sqlite has a port: the driver is
// registered as "sqlite".
const driverLinked = true
