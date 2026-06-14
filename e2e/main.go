// Command conformance exercises the SDK's public (pkg/v1) API on the host and
// exits non-zero when any behaviour that *should* work on this platform is wrong.
// It is built per-GOOS and run on each real OS by .github/workflows/e2e-vm.yml —
// the proof that the SDK works, not merely compiles, everywhere, and that where a
// platform has no native mechanic the API degrades to a uniform
// UnsupportedPlatform rather than misbehaving.
package main

import (
	"os"

	"github.com/kitsunium/sdk/e2e/checks"
	"github.com/kitsunium/sdk/e2e/harness"
)

func main() {
	//: every domain contributes a suite of public-API conformance checks.
	suites := []harness.Suite{
		checks.Codec(),
		checks.Crypto(),
		checks.Logger(),
		checks.Errs(),
		checks.Process(),
		checks.Rlimit(),
		checks.Signal(),
		checks.Cgroup(),
		checks.Reaper(),
		checks.SDNotify(),
		checks.SDListen(),
	}
	//: run them all; the Fail count is the process exit code (0 = host conforms).
	fails := harness.Run(os.Stdout, suites)
	//: a non-zero exit makes a broken kernel/behaviour fail the e2e lane.
	if fails > 0 {
		//: signal conformance failure to the CI lane.
		os.Exit(1)
	}
}
