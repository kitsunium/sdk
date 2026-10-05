//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package secret

// platformNative reports that this GOOS has every mechanic the file store
// needs. NewFile reads it before touching the disk.
const platformNative bool = true
