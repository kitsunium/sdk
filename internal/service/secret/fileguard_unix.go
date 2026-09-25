//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

// Package secret — the file store's platform gate, on the platforms that have
// what it rests on: vfs's atomic publication with a flushed directory entry,
// permission bits that exclude other accounts, and the lock domain's flock(2).
package secret

// platformNative reports that this GOOS has every mechanic the file store
// needs. NewFile reads it before touching the disk.
const platformNative bool = true
