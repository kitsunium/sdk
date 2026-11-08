//go:build !(linux && (amd64 || arm64))

package exec

import (
	"runtime"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// LandlockABI returns the Landlock ABI version the running kernel implements:
// 1 on Linux 5.13, 2 on 5.19, 3 on 6.2, 4 on 6.7, 5 on 6.10, 6 on 6.12. A
// LandlockValue's rights newer than it are refused with SandboxUnavailable
// unless named best-effort. A kernel without Landlock, or with Landlock
// disabled at boot, is SandboxUnavailable; every platform but Linux on amd64
// and arm64 is UnsupportedPlatform.
func LandlockABI() (abi int, err error) {
	//: UNSUPPORTED_PLATFORM naming the function and the platform.
	return 0, wrapUnsupportedPlatform(errs.String("feature", "LandlockABI"), errs.String("platform", runtime.GOOS+"/"+runtime.GOARCH))
}
