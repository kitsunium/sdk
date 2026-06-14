//go:build !linux

// Package cgroup — non-Linux stub: cgroup v2 is Linux-only.
package cgroup

import (
	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// available is the non-Linux stub: the unified cgroup v2 hierarchy exists only
// on Linux, so the probe always reports false.
func available() bool {
	//: no cgroup v2 off Linux.
	return false
}

// createGroup is the non-Linux stub: control groups cannot be created off
// Linux, so it returns the typed UnsupportedPlatform sentinel and no handle.
func createGroup(_ string, _ ...Option) (g coreproc.Group, err error) {
	//: the bare sentinel carries the no-cause UNSUPPORTED_PLATFORM error.
	return nil, coreproc.UnsupportedPlatform
}
