package service

import (
	"math"
	"slices"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	coresvc "github.com/kitsunium/sdk/internal/core/proc/service"
	"github.com/kitsunium/sdk/internal/kernel/backoff"
)

var (
	// cleanSignals end a daemon cleanly: it has no handler for them.
	cleanSignals = []coreproc.Signal{sigHUP, sigINT, sigTERM, sigPIPE}
	// skips are the ends no policy restarts: a skipped condition and a start
	// the limiter refused.
	skips = bit(coresvc.ResultExecCondition) | bit(coresvc.ResultStartLimitHit)
	// unclean are the ends that are neither a success nor a skip.
	unclean = ^(bit(coresvc.ResultSuccess) | skips)
	// table1 is Table 1 of systemd.service(5): the results each Restart=
	// policy restarts after, by policy.
	table1 = [...]resultBits{
		coresvc.RestartNo:         0,
		coresvc.RestartOnSuccess:  bit(coresvc.ResultSuccess),
		coresvc.RestartOnFailure:  unclean,
		coresvc.RestartOnAbnormal: unclean &^ bit(coresvc.ResultExitCode),
		coresvc.RestartOnWatchdog: bit(coresvc.ResultWatchdog),
		coresvc.RestartOnAbort:    bit(coresvc.ResultSignal) | bit(coresvc.ResultCoreDump),
		coresvc.RestartAlways:     ^skips,
	}
)

// resultBits is a set of results, one bit each.
type resultBits uint16

// shallRestart is service.c's service_shall_restart: whether a run that
// ended with result restarts, the main process having ended with exit when
// exited is true. A stop the caller asked for is decided before it.
func shallRestart(s *settings, result coresvc.Result, exit coreproc.ExitValue, exited bool) bool {
	//: RestartPreventExitStatus= never restarts.
	if exited && s.prevent.Contains(exit) {
		//: prevented.
		return false
	}
	//: RestartForceExitStatus= always restarts, but a oneshot that succeeded.
	if exited && s.force.Contains(exit) {
		//: forced.
		return s.typ != coresvc.TypeOneshot || result != coresvc.ResultSuccess
	}
	//: Table 1 of systemd.service(5).
	return restartTable(s.restart, result)
}

// bit is the set holding r alone.
func bit(r coresvc.Result) resultBits {
	//: one bit per result.
	return 1 << r
}

// restartTable is Table 1 of systemd.service(5): whether policy restarts after
// result.
func restartTable(policy coresvc.Restart, result coresvc.Result) bool {
	//: a policy no constant names never restarts.
	if int(policy) >= len(table1) {
		//: no.
		return false
	}
	//: the policy's row holds the result.
	return table1[policy]&bit(result) != 0
}

// restartDelay is service.c's service_restart_usec_next: the wait before the
// n-th automatic restart, RestartSec= grown towards RestartMaxDelaySec= over
// RestartSteps= on the kernel's backoff curve: base × ratio^(n−1), ratio =
// (max/base)^(1/steps), held at max past the steps.
func restartDelay(s *settings, n uint32) time.Duration {
	base, ceiling, steps := s.restartDelay, s.restartMax, s.restartSteps
	//: no curve: the first restart, no steps, no base, no ceiling, or a base
	//: already at the ceiling.
	if n <= 1 || steps == 0 || base == 0 || ceiling == coresvc.Infinity || base >= ceiling {
		//: RestartSec= itself.
		return base
	}
	//: past the steps the ceiling holds.
	if int64(n) > int64(steps) {
		//: RestartMaxDelaySec=.
		return ceiling
	}
	ratio := math.Pow(float64(ceiling)/float64(base), 1/float64(steps))
	//: the kernel's geometric curve, held at the ceiling.
	return backoff.Grow(base, ceiling, ratio, int(n))
}

// jitter draws a duration in [0, bound] from the event's random draw.
func jitter(draw uint64, bound time.Duration) time.Duration {
	//: no randomisation asked for.
	if bound <= 0 {
		//: none.
		return 0
	}
	//: an unbounded randomisation is taken as the longest one.
	if bound == coresvc.Infinity {
		//: a draw below the longest duration.
		return time.Duration(draw % uint64(math.MaxInt64))
	}
	//: uniform enough for a delay: the modulo bias is below one draw in 2^32.
	return time.Duration(draw % (uint64(bound) + 1))
}

// addSaturating adds two non-negative durations, held at Infinity.
func addSaturating(a, b time.Duration) time.Duration {
	//: past the longest duration.
	if a >= coresvc.Infinity-b {
		//: Infinity.
		return coresvc.Infinity
	}
	//: the sum.
	return a + b
}

// classifyMain is service.c's reading of a main process's end: clean is
// success, "-" makes any end a success, an adopted process gone without a
// status is clean (ADR 0214 §10), else an exit code or a signal.
func classifyMain(s *settings, exit coreproc.ExitValue, unknown bool, flags coresvc.CommandFlags) coresvc.Result {
	//: gone without a status is clean, as systemd reads a vanished main PID,
	//: and "-" reads every end as a success.
	if unknown || flags&coresvc.FlagIgnoreFailure != 0 || isClean(exit, &s.success, s.typ != coresvc.TypeOneshot) {
		//: success.
		return coresvc.ResultSuccess
	}
	//: the unclean ends.
	return uncleanResult(exit)
}

// classifyControl reads a command's end: exit 0 is success, "-" makes any end
// one; SuccessExitStatus= and the clean signals are the main process's only.
func classifyControl(exit coreproc.ExitValue, unknown bool, flags coresvc.CommandFlags) coresvc.Result {
	//: "-", an unknown end, or a clean exit code.
	if unknown || flags&coresvc.FlagIgnoreFailure != 0 || isClean(exit, nil, false) {
		//: success.
		return coresvc.ResultSuccess
	}
	//: the unclean ends.
	return uncleanResult(exit)
}

// uncleanResult names an unclean end: a signal, or an exit code. A core dump
// reads as a signal until ExitValue carries the dump bit (#1090).
func uncleanResult(exit coreproc.ExitValue) coresvc.Result {
	//: a signal ended it.
	if exit.Signaled {
		//: signal.
		return coresvc.ResultSignal
	}
	//: an exit code.
	return coresvc.ResultExitCode
}

// isClean is systemd's is_clean_exit: exit 0, a status of success, or — for a
// daemon — SIGHUP, SIGINT, SIGTERM or SIGPIPE.
func isClean(exit coreproc.ExitValue, success *coresvc.ExitStatusValue, daemon bool) bool {
	//: an exit code.
	if !exit.Signaled {
		//: zero, or a status listed as success.
		return exit.Code == 0 || (success != nil && success.Contains(exit))
	}
	//: a daemon that has no handler for the four is not unclean, and a
	//: signal listed as success is clean.
	return (daemon && slices.Contains(cleanSignals, exit.Signal)) || (success != nil && success.Contains(exit))
}
