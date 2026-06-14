// Package harness is the tiny test-runner the SDK conformance binary is built on.
// A Check exercises one public-API behaviour on the real host and returns a
// Result; a Suite groups a domain's checks; Run executes every suite, prints a
// per-check table, and reports whether any supported check failed. The point is
// to prove the SDK does not merely *compile* on a platform but actually *works*
// there (cgroups enforce, the rlimit trampoline caps fds, signals deliver, …),
// and that where a platform has no native mechanic the public API degrades to a
// uniform UnsupportedPlatform rather than misbehaving.
package harness

import (
	"fmt"
	"io"
	"sort"
)

// Status is the outcome of a single conformance check.
type Status string

// The four possible check outcomes.
const (
	// Pass — the behaviour was exercised and the observable effect was correct.
	Pass Status = "PASS"
	// Fail — the behaviour should work on this platform but the effect was wrong.
	Fail Status = "FAIL"
	// Unsupported — the platform legitimately has no native mechanic; the public
	// API returned the uniform UnsupportedPlatform contract (this is a success).
	Unsupported Status = "UNSUPPORTED"
	// Skip — the check could not run for an environmental reason (e.g. no /bin/sh,
	// no cgroup write permission) and makes no claim either way.
	Skip Status = "SKIP"
)

// Result is the outcome of one named check within a domain.
type Result struct {
	// Domain is the SDK area under test (codec, crypto, process, cgroup, …).
	Domain string
	// Name is the specific behaviour exercised.
	Name string
	// Status is the outcome.
	Status Status
	// Detail is a short human explanation (the observed value, the error, why
	// it was skipped). Always set it so a failure is diagnosable from the table.
	Detail string
}

// Check exercises one public-API behaviour and returns its Result. A Check must
// not panic; it converts any failure into a Fail/Skip Result with detail.
type Check func() Result

// Suite is a domain's named group of checks, returned by each checks/<domain>.go.
type Suite struct {
	// Domain labels every Result the suite produces.
	Domain string
	// Checks are run in order; each is independent.
	Checks []Check
}

// Pass builds a passing Result for the given domain/name.
func Passed(domain, name, detail string) Result {
	//: a correct, exercised behaviour.
	return Result{Domain: domain, Name: name, Status: Pass, Detail: detail}
}

// Failed builds a failing Result.
func Failed(domain, name, detail string) Result {
	//: a behaviour that should have worked here but did not.
	return Result{Domain: domain, Name: name, Status: Fail, Detail: detail}
}

// NotSupported builds an UnsupportedPlatform (expected-on-this-OS) Result.
func NotSupported(domain, name, detail string) Result {
	//: the uniform off-platform contract — a success, not a failure.
	return Result{Domain: domain, Name: name, Status: Unsupported, Detail: detail}
}

// Skipped builds a Skip Result for an environmental gap (no shell, no perms).
func Skipped(domain, name, detail string) Result {
	//: the check made no claim; the environment could not host it.
	return Result{Domain: domain, Name: name, Status: Skip, Detail: detail}
}

// Run executes every suite, writes a per-check table to out, and returns the
// number of Fail results (0 means the host conforms). UNSUPPORTED and SKIP never
// count as failures.
func Run(out io.Writer, suites []Suite) int {
	var results []Result
	//: collect every check's result across all domains first.
	for _, suite := range suites {
		//: run each check in the suite, guarding against a panicking check.
		for _, check := range suite.Checks {
			//: a check that panics is recorded as a Fail, never aborts the run.
			results = append(results, safeRun(suite.Domain, check))
		}
	}
	//: stable ordering so diffs across machines line up.
	sort.SliceStable(results, func(i, j int) bool {
		//: order by domain then name for a deterministic table.
		if results[i].Domain != results[j].Domain {
			//: primary key is the domain.
			return results[i].Domain < results[j].Domain
		}
		//: secondary key is the check name.
		return results[i].Name < results[j].Name
	})
	return report(out, results)
}

// safeRun executes a single check, converting a panic into a Fail Result so one
// misbehaving check cannot take down the whole conformance run.
func safeRun(domain string, check Check) (res Result) {
	//: recover turns a panicking check into a diagnosable Fail.
	defer func() {
		//: only a non-nil recover indicates the check panicked.
		if r := recover(); r != nil {
			//: record the panic value as the failure detail.
			res = Failed(domain, "panic", fmt.Sprintf("%v", r))
		}
	}()
	//: run the check; its Result is returned unless it panics.
	return check()
}

// report prints the table + a summary line and returns the Fail count.
func report(out io.Writer, results []Result) int {
	fails, passes, unsup, skips := 0, 0, 0, 0
	//: print one row per check and tally the outcomes.
	for _, r := range results {
		//: a fixed-width row keeps the table readable across terminals.
		stderrf(out, "%-11s %-11s %-34s %s\n", r.Domain, r.Status, r.Name, r.Detail)
		//: tally each outcome for the summary line.
		switch r.Status {
		//: a correct behaviour.
		case Pass:
			//: count a pass.
			passes++
		//: a real failure.
		case Fail:
			//: count a fail.
			fails++
		//: an expected off-platform result.
		case Unsupported:
			//: count an unsupported.
			unsup++
		//: an environmental skip.
		default:
			//: count a skip.
			skips++
		}
	}
	stderrf(out, "\nsummary: %d pass · %d fail · %d unsupported · %d skip\n", passes, fails, unsup, skips)
	return fails
}

// stderrf is a fmt.Fprintf that drops the write error — the report is best-effort
// diagnostic output, and the process exit code (not the print) is the contract.
func stderrf(out io.Writer, format string, args ...any) {
	//: best-effort print; a write fault does not change the conformance verdict.
	if _, err := fmt.Fprintf(out, format, args...); err != nil {
		//: nothing to do — the exit code carries the result.
		return
	}
}
