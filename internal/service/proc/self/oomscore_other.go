//go:build !linux

package self

import (
	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// setOOMScoreAdj refuses off Linux; see SetOOMScoreAdj.
func setOOMScoreAdj(_ int) error {
	//: the OOM-killer bias is a Linux procfs knob.
	return coreproc.UnsupportedPlatform
}
