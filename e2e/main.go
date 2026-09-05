// Package main — the conformance binary exercises the SDK's public (pkg/v1) API
// on the host and exits non-zero when any behaviour that *should* work on this
// platform is wrong.
//
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

// main runs every domain's conformance group and turns the Fail count into the
// process exit code, which is what makes a broken kernel fail the e2e lane
// rather than merely print a table nobody reads.
func main() {
	//: run every registered group; the Fail count is the process exit code
	//: (0 = the host conforms).
	fails := harness.Run(os.Stdout, conformanceGroups())
	//: a non-zero exit makes a broken kernel/behaviour fail the e2e lane.
	if fails > 0 {
		//: signal conformance failure to the CI lane.
		os.Exit(1)
	}
}

// conformanceGroups is every domain's group, in registration order.
//
// It is a function rather than a literal inside main so the registration itself
// can be asserted: a domain that is built and then not listed here contributes
// no rows at all, and the table shows nothing missing.
func conformanceGroups() []harness.CheckGroup {
	//: every domain contributes a group of public-API conformance checks.
	return []harness.CheckGroup{
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
}
