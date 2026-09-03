//go:build !linux && !darwin && !freebsd && !netbsd && !openbsd && !dragonfly

// Package server — the SO_REUSEPORT floor for platforms without it.
package server

// reusePortSupported reports whether this platform can bind several listeners
// to one address.
func reusePortSupported() bool {
	//: Windows has SO_REUSEADDR with entirely different semantics — it permits
	//: hijacking a bound port rather than load-balancing across sockets — so it
	//: is deliberately NOT used as a substitute.
	return false
}

// setReusePort is a no-op where the option does not exist.
func setReusePort(_ uintptr) error {
	//: the caller only reaches this when reusePortSupported already said no.
	return nil
}
