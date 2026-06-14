// Package checks — process-supervision domain. This file holds the process,
// rlimit, and signal conformance suites: it spawns a real shell to prove
// Start/Wait exit-status decoding, StdioCapture delivery, group-aware Stop, the
// setrlimit/umask re-exec trampoline, and the portable signal Parse/String/Relay
// contracts on the host kernel (UnsupportedPlatform off Unix).
package checks

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	perrs "github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/process"
	"github.com/kitsunium/sdk/pkg/v1/rlimit"
	"github.com/kitsunium/sdk/pkg/v1/signal"

	"github.com/kitsunium/sdk/e2e/harness"
)

// processDomain / rlimitDomain / signalDomain label every Result the three
// process-supervision suites emit.
const (
	processDomain string = "process"
	rlimitDomain  string = "rlimit"
	signalDomain  string = "signal"
)

// shellPath is the POSIX shell every process/rlimit check spawns; its absence is
// an environmental gap (Skip), not a conformance failure.
const shellPath string = "/bin/sh"

// decBase / octBase are the strconv bases the shell output is parsed/formatted in
// (decimal for ulimit counts, octal for the umask the shell prints).
const (
	decBase int = 10
	octBase int = 8
)

// umaskBits is the integer width (bits) the octal umask string is parsed into;
// a umask fits comfortably in a 32-bit value.
const umaskBits int = 32

// wantExitCode is the status `exit 7` must surface through ExitResult.Code,
// proving the wait4 path decodes the child's real exit status.
const wantExitCode int = 7

// stopGrace is the SIGTERM→SIGKILL escalation window Stop is given for the
// group-terminate check; a sleeping shell exits on the first SIGTERM.
const stopGrace time.Duration = 2 * time.Second

// relayDeadline bounds the signal.Relay drain so a regression that blocks cannot
// hang the whole conformance run; a pre-closed source channel drains at once.
const relayDeadline time.Duration = 2 * time.Second

// intPtr returns a pointer to v — for the pointer Spec fields (Umask) without a
// named local at the call site.
func intPtr(v int) *int {
	return &v
}

// wantNoFile is the open-file ceiling pushed through Spec.Rlimits and read back
// via `ulimit -n` — proof the re-exec trampoline ran setrlimit on this kernel.
const wantNoFile uint64 = 64

// wantUmask is the file-creation mask pushed through Spec.Umask and read back via
// `umask` (octal) — proof the trampoline ran umask(2) in the fresh child.
const wantUmask int = 0o077

// wantCore is the core-dump ceiling (0/0) pushed through Spec.Rlimits and read
// back via `ulimit -c` — proof the trampoline applied an RLIMIT_CORE of zero.
const wantCore uint64 = 0

// wantStdout is the exact byte sequence `echo hello` writes; StdioCapture must
// deliver it verbatim (including the trailing newline) before Wait returns.
const wantStdout string = "hello\n"

// The check-name labels each Result carries within its domain; declared at
// package level so no check body holds a local const block.
const (
	// nameExitCode labels the Start/Wait exit-status check.
	nameExitCode string = "exit-code"
	// nameStdioCapture labels the StdioCapture delivery check.
	nameStdioCapture string = "stdio-capture"
	// nameGroupStop labels the Setpgid + group-aware Stop check.
	nameGroupStop string = "group-stop"
	// nameNoFile labels the RLIMIT_NOFILE trampoline check.
	nameNoFile string = "nofile"
	// nameUmask labels the umask(2) trampoline check.
	nameUmask string = "umask"
	// nameCore labels the RLIMIT_CORE trampoline check.
	nameCore string = "core"
	// nameParse labels the Parse/String/value round-trip check.
	nameParse string = "parse-roundtrip"
	// nameRelay labels the Relay termination-contract check.
	nameRelay string = "relay-delivery"
)

// nofileLimits / coreLimits are the Spec.Rlimits sets the rlimit checks apply,
// hoisted to package level so the check bodies allocate no constant map per call.
var (
	// nofileLimits caps the open-file soft/hard ceiling at wantNoFile.
	nofileLimits = map[rlimit.Resource]rlimit.Limit{
		rlimit.ResourceNoFile: {Soft: wantNoFile, Hard: wantNoFile},
	}
	// coreLimits caps the core-dump soft/hard ceiling at zero.
	coreLimits = map[rlimit.Resource]rlimit.Limit{
		rlimit.ResourceCore: {Soft: wantCore, Hard: wantCore},
	}
)

// shArgv builds the argv for `sh -c <script>` — argv[0] plus the -c flag and the
// script. Centralising it keeps every check off a bare constant slice literal.
func shArgv(script string) []string {
	//: the conventional non-login shell invocation: argv0, -c, then the script.
	return []string{"sh", "-c", script}
}

// unsupportedProc reports whether err is the uniform off-platform contract from
// the proc domain; on such a host the check is NotSupported, not a failure.
func unsupportedProc(err error) bool {
	//: the whole proc domain reports off-platform via this one central sentinel.
	return perrs.HasCode(err, coreproc.CodeUnsupportedPlatform)
}

// haveShell reports whether the POSIX shell the spawn checks need is present.
func haveShell() bool {
	_, err := os.Stat(shellPath)
	//: a missing /bin/sh is an environmental gap that makes the check a Skip.
	return err == nil
}

// Process returns the process-spawn conformance checks (Start/Wait exit codes,
// stdio capture, group-aware Stop) — Unix; UnsupportedPlatform off Unix.
func Process() harness.Suite {
	//: three independent behaviours, each self-reporting off-platform/no-shell.
	return harness.Suite{
		Domain: processDomain,
		Checks: []harness.Check{
			processExitCode,
			processStdioCapture,
			processGroupStop,
		},
	}
}

// processExitCode spawns `sh -c 'exit 7'`, waits, and asserts the decoded exit
// code is 7 — the proof that Start/Wait surface the child's real status.
func processExitCode() harness.Result {
	//: no shell on this host means the check cannot run; it makes no claim.
	if !haveShell() {
		//: environmental gap, not a conformance verdict.
		return harness.Skipped(processDomain, nameExitCode, "no "+shellPath)
	}
	spec := process.Spec{
		Path: shellPath,
		Args: shArgv("exit " + strconv.Itoa(wantExitCode)),
	}
	proc, err := process.Start(context.Background(), spec)
	//: a Start error is either the off-platform contract or a real failure.
	if err != nil {
		//: UnsupportedPlatform is the expected, correct result off Unix.
		if unsupportedProc(err) {
			//: degrade uniformly rather than fail on a platform without spawn.
			return harness.NotSupported(processDomain, nameExitCode, err.Error())
		}
		//: any other Start error on a host that should spawn is a failure.
		return harness.Failed(processDomain, nameExitCode, "start: "+err.Error())
	}
	exit, err := proc.Wait()
	//: Wait must succeed; a wait4 fault is a real conformance failure here.
	if err != nil {
		//: a failed Wait cannot prove the exit code path works.
		return harness.Failed(processDomain, nameExitCode, "wait: "+err.Error())
	}
	//: the decoded status must equal the code the shell exited with.
	if exit.Code != wantExitCode {
		//: a mismatched code means the wait4 status decode is wrong on this kernel.
		return harness.Failed(processDomain, nameExitCode,
			"code="+strconv.Itoa(exit.Code)+" want "+strconv.Itoa(wantExitCode))
	}
	//: a correctly decoded non-zero exit status.
	return harness.Passed(processDomain, nameExitCode, "exit code "+strconv.Itoa(wantExitCode))
}

// processStdioCapture spawns `sh -c 'echo hello'` under StdioCapture and asserts
// the captured buffer is exactly "hello\n" — proof stdio capture delivers every
// byte on the real kernel before Wait returns.
func processStdioCapture() harness.Result {
	//: no shell on this host means the check cannot run; it makes no claim.
	if !haveShell() {
		//: environmental gap, not a conformance verdict.
		return harness.Skipped(processDomain, nameStdioCapture, "no "+shellPath)
	}
	var buf bytes.Buffer
	spec := process.Spec{
		Path:   shellPath,
		Args:   shArgv("echo hello"),
		Stdio:  process.StdioCapture,
		Stdout: &buf,
	}
	proc, err := process.Start(context.Background(), spec)
	//: a Start error is either the off-platform contract or a real failure.
	if err != nil {
		//: UnsupportedPlatform is the expected, correct result off Unix.
		if unsupportedProc(err) {
			//: degrade uniformly rather than fail on a platform without spawn.
			return harness.NotSupported(processDomain, nameStdioCapture, err.Error())
		}
		//: any other Start error on a host that should spawn is a failure.
		return harness.Failed(processDomain, nameStdioCapture, "start: "+err.Error())
	}
	//: Wait drains the copier goroutines, so the buffer is complete after it.
	if _, werr := proc.Wait(); werr != nil {
		//: a failed Wait cannot prove the capture path delivered the bytes.
		return harness.Failed(processDomain, nameStdioCapture, "wait: "+werr.Error())
	}
	//: every byte the child wrote must have reached the caller's writer.
	if got := buf.String(); got != wantStdout {
		//: a mismatched buffer means stdio capture dropped or mangled output.
		return harness.Failed(processDomain, nameStdioCapture, "captured "+strconv.Quote(got))
	}
	//: an exact-match capture proves stdio delivery on this kernel.
	return harness.Passed(processDomain, nameStdioCapture, "captured "+strconv.Quote(wantStdout))
}

// processGroupStop spawns `sh -c 'sleep 30'` in its own group and asserts a
// group-aware Stop terminates it — proof Setpgid + Stop reach the whole tree.
func processGroupStop() harness.Result {
	//: no shell on this host means the check cannot run; it makes no claim.
	if !haveShell() {
		//: environmental gap, not a conformance verdict.
		return harness.Skipped(processDomain, nameGroupStop, "no "+shellPath)
	}
	spec := process.Spec{
		Path:    shellPath,
		Args:    shArgv("sleep 30"),
		Setpgid: true,
	}
	proc, err := process.Start(context.Background(), spec)
	//: a Start error is either the off-platform contract or a real failure.
	if err != nil {
		//: UnsupportedPlatform is the expected, correct result off Unix.
		if unsupportedProc(err) {
			//: degrade uniformly rather than fail on a platform without spawn.
			return harness.NotSupported(processDomain, nameGroupStop, err.Error())
		}
		//: any other Start error on a host that should spawn is a failure.
		return harness.Failed(processDomain, nameGroupStop, "start: "+err.Error())
	}
	//: Stop sends SIGTERM to the group and must return nil once it has exited.
	if serr := proc.Stop(context.Background(), stopGrace, process.SIGTERM); serr != nil {
		//: a non-nil Stop means the group outlived the escalation — a failure.
		return harness.Failed(processDomain, nameGroupStop, "stop: "+serr.Error())
	}
	//: verify the leader actually terminated rather than trusting Stop's return:
	//: Stop drove the once-only reap, so Wait returns the cached exit status.
	exit, werr := proc.Wait()
	//: a Wait fault here means the termination could not be confirmed.
	if werr != nil {
		//: the group-stop cannot be proven without the reaped status.
		return harness.Failed(processDomain, nameGroupStop, "wait after stop: "+werr.Error())
	}
	//: a clean (zero, non-signalled) exit would mean the sleeper was NOT killed.
	if exit.Success() {
		//: the leader exited 0 — Stop did not actually terminate it.
		return harness.Failed(processDomain, nameGroupStop, "leader survived Stop (clean exit)")
	}
	//: a confirmed non-clean exit proves Setpgid + escalation killed the tree.
	return harness.Passed(processDomain, nameGroupStop, fmt.Sprintf("group terminated (code=%d signal=%v)", exit.Code, exit.Signal))
}

// Rlimit returns the rlimit/umask conformance checks: spawn a shell that prints
// its effective limit (ulimit -n, umask, ulimit -c) under Spec.Rlimits/Umask and
// assert the trampoline actually applied it — Unix; UnsupportedPlatform off Unix.
func Rlimit() harness.Suite {
	//: three trampoline-applied attributes, each read back from the live child.
	return harness.Suite{
		Domain: rlimitDomain,
		Checks: []harness.Check{
			rlimitNoFile,
			rlimitUmask,
			rlimitCore,
		},
	}
}

// captureShell spawns `sh -c <script>` under the given limits/umask with stdout
// captured, waits, and returns the trimmed stdout. The bool reports whether the
// returned Result is terminal (NotSupported/Skip/Fail); when it is false the
// caller proceeds to its own assertion on the returned string.
func captureShell(domain, name, script string, limits map[rlimit.Resource]rlimit.Limit, umask *int) (string, harness.Result, bool) {
	//: no shell on this host means the check cannot run; it makes no claim.
	if !haveShell() {
		//: environmental gap, not a conformance verdict.
		return "", harness.Skipped(domain, name, "no "+shellPath), true
	}
	var buf bytes.Buffer
	spec := process.Spec{
		Path:    shellPath,
		Args:    shArgv(script),
		Stdio:   process.StdioCapture,
		Stdout:  &buf,
		Rlimits: limits,
		Umask:   umask,
	}
	proc, err := process.Start(context.Background(), spec)
	//: a Start error is either the off-platform contract or a real failure.
	if err != nil {
		//: UnsupportedPlatform is the expected, correct result off the trampoline.
		if unsupportedProc(err) {
			//: degrade uniformly rather than fail where the trampoline cannot run.
			return "", harness.NotSupported(domain, name, err.Error()), true
		}
		//: any other Start error on a host that should spawn is a failure.
		return "", harness.Failed(domain, name, "start: "+err.Error()), true
	}
	//: Wait drains the captured stdout, so the buffer is complete after it.
	if _, werr := proc.Wait(); werr != nil {
		//: a failed Wait cannot prove the limit was applied and printed.
		return "", harness.Failed(domain, name, "wait: "+werr.Error()), true
	}
	//: the shell prints exactly one value plus a newline; trim it for compare.
	return strings.TrimSpace(buf.String()), harness.Result{}, false
}

// rlimitNoFile spawns `sh -c 'ulimit -n'` under an RLIMIT_NOFILE of 64/64 and
// asserts the shell reports 64 — proof the re-exec trampoline ran setrlimit.
func rlimitNoFile() harness.Result {
	got, res, done := captureShell(rlimitDomain, nameNoFile, "ulimit -n", nofileLimits, nil)
	//: a terminal helper Result short-circuits the assertion.
	if done {
		//: forward the NotSupported/Skip/Fail outcome verbatim.
		return res
	}
	want := strconv.FormatUint(wantNoFile, decBase)
	//: the shell's reported soft fd ceiling must equal the limit we applied.
	if got != want {
		//: a mismatch means the trampoline did not cap the child's fds.
		return harness.Failed(rlimitDomain, nameNoFile, "ulimit -n="+got+" want "+want)
	}
	//: a matching ceiling proves the setrlimit reached the child.
	return harness.Passed(rlimitDomain, nameNoFile, "ulimit -n="+want)
}

// rlimitUmask spawns `sh -c 'umask'` under Spec.Umask of 0o077 and asserts the
// shell reports that octal mask — proof the trampoline ran umask(2).
func rlimitUmask() harness.Result {
	got, res, done := captureShell(rlimitDomain, nameUmask, "umask", nil, intPtr(wantUmask))
	//: a terminal helper Result short-circuits the assertion.
	if done {
		//: forward the NotSupported/Skip/Fail outcome verbatim.
		return res
	}
	//: the shell prints umask in octal; parse it back to compare numerically.
	parsed, perr := strconv.ParseUint(got, octBase, umaskBits)
	//: an unparseable mask means the shell printed something unexpected.
	if perr != nil {
		//: report the raw token so the malformed value is diagnosable.
		return harness.Failed(rlimitDomain, nameUmask, "umask "+strconv.Quote(got)+" not octal")
	}
	//: the applied mask must round-trip through the child's reported umask.
	if int(parsed) != wantUmask {
		//: a mismatch means the trampoline did not set the file-creation mask.
		return harness.Failed(rlimitDomain, nameUmask,
			"umask=0o"+strconv.FormatUint(parsed, octBase)+
				" want 0o"+strconv.FormatInt(int64(wantUmask), octBase))
	}
	//: a matching octal mask proves the umask(2) reached the child.
	return harness.Passed(rlimitDomain, nameUmask, "umask=0o"+strconv.FormatInt(int64(wantUmask), octBase))
}

// rlimitCore spawns `sh -c 'ulimit -c'` under an RLIMIT_CORE of 0/0 and asserts
// the shell reports 0 — proof the trampoline applied the core-dump ceiling.
func rlimitCore() harness.Result {
	got, res, done := captureShell(rlimitDomain, nameCore, "ulimit -c", coreLimits, nil)
	//: a terminal helper Result short-circuits the assertion.
	if done {
		//: forward the NotSupported/Skip/Fail outcome verbatim.
		return res
	}
	want := strconv.FormatUint(wantCore, decBase)
	//: the shell's reported core-dump ceiling must equal the limit we applied.
	if got != want {
		//: a mismatch means the trampoline did not cap the child's core dumps.
		return harness.Failed(rlimitDomain, nameCore, "ulimit -c="+got+" want "+want)
	}
	//: a matching ceiling proves the RLIMIT_CORE reached the child.
	return harness.Passed(rlimitDomain, nameCore, "ulimit -c="+want)
}

// Signal returns the signal-domain conformance checks (Parse/String round-trip,
// numeric value matches the platform, Relay delivery) — portable Parse/String;
// Relay is UnsupportedPlatform off Unix.
func Signal() harness.Suite {
	//: Parse/String are portable value behaviour; Relay self-reports off-platform.
	return harness.Suite{
		Domain: signalDomain,
		Checks: []harness.Check{
			signalParse,
			signalRelay,
		},
	}
}

// signalParse asserts both "SIGTERM" and "TERM" resolve, String round-trips, and
// the numeric value matches the platform SIGTERM — portable on every GOOS.
func signalParse() harness.Result {
	long, lerr := signal.Parse("SIGTERM")
	//: the canonical name must resolve on every platform with a signal table.
	if lerr != nil {
		//: a Parse failure here means the portable name table is wrong.
		return harness.Failed(signalDomain, nameParse, "Parse(SIGTERM): "+lerr.Error())
	}
	short, serr := signal.Parse("TERM")
	//: the bare form must resolve to the identical value.
	if serr != nil {
		//: a Parse failure on the bare form means prefix-normalisation broke.
		return harness.Failed(signalDomain, nameParse, "Parse(TERM): "+serr.Error())
	}
	//: both spellings must name the same signal value.
	if long != short {
		//: divergent values mean SIGTERM and TERM resolved differently.
		return harness.Failed(signalDomain, nameParse, "SIGTERM != TERM")
	}
	//: String must round-trip back to the canonical name.
	if long.String() != "SIGTERM" {
		//: a broken String means the reverse lookup is wrong on this platform.
		return harness.Failed(signalDomain, nameParse, "String="+long.String())
	}
	//: the typed value must equal the host kernel's SIGTERM number.
	if long != process.SIGTERM {
		//: a numeric mismatch means the table disagrees with the platform syscall.
		return harness.Failed(signalDomain, nameParse,
			"value="+strconv.Itoa(long.Int())+" != platform SIGTERM "+strconv.Itoa(process.SIGTERM.Int()))
	}
	//: a full name/bare/numeric round-trip proves the portable signal table.
	return harness.Passed(signalDomain, nameParse, "SIGTERM/TERM round-trip, value "+strconv.Itoa(long.Int()))
}

// signalRelay drives Relay's termination contract: a pre-closed source channel
// makes Relay drain immediately with no real delivery, so the check observes the
// portable nil-on-clean-drain contract without racing a signal into this process
// — UnsupportedPlatform off Unix.
func signalRelay() harness.Result {
	//: a pre-closed source channel makes Relay drain at once with no kill(2),
	//: so the check is deterministic and never signals the test process itself.
	src := make(chan signal.Signal)
	close(src)
	//: run Relay under a bounded deadline so a regression that blocks cannot hang
	//: the conformance run; the pre-closed source makes the happy path immediate.
	relayed := make(chan error, 1)
	go func() { relayed <- signal.Relay(src, signal.Target(os.Getpid())) }()
	var err error
	//: take whichever happens first: Relay returning, or the deadline elapsing.
	select {
	//: Relay returned within the deadline — classify its error below.
	case err = <-relayed:
		//: fall through to the shared error classification.
	//: Relay never returned — a regression, reported as a Skip not a hang.
	case <-time.After(relayDeadline):
		//: degrade to a Skip rather than block the whole conformance run.
		return harness.Skipped(signalDomain, nameRelay, fmt.Sprintf("Relay did not return within %s", relayDeadline))
	}
	//: off Unix Relay cannot kill(2); the uniform contract is the correct result.
	if err != nil && unsupportedProc(err) {
		//: degrade uniformly rather than fail where kill(2) is unavailable.
		return harness.NotSupported(signalDomain, nameRelay, err.Error())
	}
	//: on Unix a clean drain of a closed source returns nil — that is the contract.
	if err != nil {
		//: a non-nil drain on Unix means Relay's termination contract broke.
		return harness.Failed(signalDomain, nameRelay, "relay: "+err.Error())
	}
	//: a clean drain proves Relay's portable termination contract holds.
	return harness.Passed(signalDomain, nameRelay, "relay drained cleanly")
}
