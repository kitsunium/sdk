//go:build linux || solaris

package rlimit

// selfFDDir is the caller's descriptor directory in procfs.
const selfFDDir string = "/proc/self/fd"
