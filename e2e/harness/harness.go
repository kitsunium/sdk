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
	"runtime"
	"slices"
	"strings"
	"time"
)

// CheckTimeout bounds how long Run waits for one check.
//
// Every check is written never to hang — bounded deadlines on every blocking
// receive — and on real kernels one hung anyway: the debian-systemd and
// fedora-systemd legs of e2e-vm.yml ran until the job's 25-minute cap cancelled
// them, and because the table is printed only once every check has returned,
// the log said nothing about which check it was (#118). A check still running
// after this is recorded as a Fail and every goroutine's stack is written out,
// which names the function that is stuck; the run then moves on. The goroutine
// is abandoned, not stopped — Go cannot stop one — which a process that is
// about to print its table and exit can afford.
//
// A minute is generous: a check's own deadlines are seconds (sd_notify's
// receive waits 2 s), and a real kernel runs the whole group in seconds.
const CheckTimeout time.Duration = time.Minute

// maxStackDump caps the goroutine dump a hung check writes. A process whose
// goroutines outgrow it has a larger problem than this dump can show, and an
// unbounded buffer would turn one hang into an out-of-memory as well.
const maxStackDump int = 16 << 20

// initialStackDump is the first buffer runtime.Stack is given; it doubles until
// the dump fits or reaches maxStackDump.
const initialStackDump int = 64 << 10

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
//
// Every field is filled by the constructors below rather than by the checks
// themselves, so a Result can never carry a status the runner does not know how
// to tally — and Detail is always set, because a failure nobody can diagnose
// from the table is a failure that will be re-investigated from scratch.
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

// Passed builds a passing Result for the given domain/name.
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
// count as failures. A check that runs past CheckTimeout is a Fail, reported
// with every goroutine's stack.
func Run(out io.Writer, groups []CheckGroup) int {
	//: the documented bound; the suite drives runWithin with a shorter one.
	return runWithin(out, groups, CheckTimeout)
}

// runWithin is Run with the per-check bound as a parameter.
func runWithin(out io.Writer, groups []CheckGroup, timeout time.Duration) int {
	var results []Result
	//: collect every check's result across all domains first.
	for _, group := range groups {
		//: run each check in the group, guarding against a panic and a hang.
		for index, check := range group.Checks {
			// A Check carries its name only in the Result it returns, so a
			// check that never returns is named by its position in its group.
			label := fmt.Sprintf("check %d of %d", index+1, len(group.Checks))
			//: a check that panics or hangs is recorded as a Fail, never aborts
			//: the run and never silences it.
			results = append(results, watchedRun(out, group.Domain, label, check, timeout))
		}
	}
	//: stable ordering so diffs across machines line up.
	slices.SortStableFunc(results, func(a, b Result) int {
		//: primary key is the domain, secondary the check name.
		if byDomain := strings.Compare(a.Domain, b.Domain); byDomain != 0 {
			//: different domains order by domain alone.
			return byDomain
		}
		//: within a domain, order by check name.
		return strings.Compare(a.Name, b.Name)
	})
	//: the table is printed and the Fail count is the process exit code.
	return report(out, results)
}

// watchedRun runs one check under safeRun and gives up on it after timeout.
//
// A check that never returns is recorded under label — its position in its
// group — and the goroutine dump written to out names the function. The
// channel is buffered, so the abandoned goroutine can still deliver its Result
// and exit if the check ever returns.
func watchedRun(out io.Writer, domain, label string, check Check, timeout time.Duration) Result {
	done := make(chan Result, 1)
	go func() {
		//: the verdict, or a Fail if the check panicked.
		done <- safeRun(domain, check)
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	//: whichever comes first: the check's own verdict or the bound.
	select {
	//: the check returned in time.
	case res := <-done:
		//: its own verdict.
		return res
	//: the check is still running.
	case <-timer.C:
		stderrf(out, "\n%s: %s did not finish within %s; every goroutine follows, the stuck check among them\n\n%s\n",
			domain, label, timeout, allStacks())
		//: a hang is a failure on this kernel, and it now has a name.
		return Failed(domain, label, fmt.Sprintf("did not finish within %s (hung); the goroutine dump precedes the table", timeout))
	}
}

// allStacks returns the stack of every goroutine, growing its buffer until the
// dump fits or reaches maxStackDump.
func allStacks() []byte {
	buf := make([]byte, initialStackDump)
	//: runtime.Stack truncates silently, so a full buffer means "grow and retry".
	for {
		n := runtime.Stack(buf, true)
		//: the dump fitted, or the cap is reached and it is as large as allowed.
		if n < len(buf) || len(buf) >= maxStackDump {
			//: the dump as captured.
			return buf[:n]
		}
		buf = make([]byte, 2*len(buf))
	}
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
	//: run the check; its Result is returned unless it panics, in which case
	//: the deferred recover above has already replaced it.
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
	//: only Fail counts against the host; UNSUPPORTED and SKIP make no claim.
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
