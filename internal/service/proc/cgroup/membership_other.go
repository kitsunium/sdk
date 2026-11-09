//go:build !linux

package cgroup

import (
	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// membership is refused where no unified hierarchy exists: no process has a
// cgroup v2 path to name.
func membership(_ string) (string, error) {
	//: the uniform answer of a platform without the mechanic.
	return "", coreproc.UnsupportedPlatform
}
