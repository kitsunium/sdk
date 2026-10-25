package server

import (
	"runtime"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// readPlatform names what the server runs on: the GOOS, the GOARCH, the
// kernel's release as the platform reports it (kernelRelease, per GOOS) and
// the toolchain. It is read once, by New.
func readPlatform() corenet.PlatformValue {
	//: read once.
	return corenet.PlatformValue{
		GOOS: runtime.GOOS, GOARCH: runtime.GOARCH,
		Kernel: kernelRelease(), Go: runtime.Version(),
	}
}
