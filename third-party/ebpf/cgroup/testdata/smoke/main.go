// Command smoke is the program TestGarbleTinyBuildLinksTheBackend builds
// with garble -tiny: it links the backend the way a consumer does, by a
// blank import, and asks the core to probe. It exits 0 when the backend
// answered — nil, LOAD_REFUSED or UNSUPPORTED_PLATFORM, each typed — and 1
// when the core says no backend is linked, or the answer lost its code.
package main

import (
	"os"

	"github.com/kitsunium/sdk/internal/kernel/errs"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	corebpf "github.com/kitsunium/sdk/internal/core/proc/bpf"
	svcbpf "github.com/kitsunium/sdk/internal/service/proc/bpf"

	_ "github.com/kitsunium/sdk/third-party/ebpf/cgroup"
)

func main() {
	err := svcbpf.Probe()
	//: the backend's answers, each with its code.
	switch {
	case err == nil:
		_, _ = os.Stdout.WriteString("linked: loads\n")
	case errs.HasCode(err, corebpf.CodeLoadRefused), errs.HasCode(err, coreproc.CodeUnsupportedPlatform):
		_, _ = os.Stdout.WriteString("linked: " + err.Error() + "\n")
	default:
		_, _ = os.Stderr.WriteString("not linked: " + err.Error() + "\n")
		os.Exit(1)
	}
}
