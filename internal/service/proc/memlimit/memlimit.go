// Package memlimit derives the Go runtime soft memory limit from the control
// group allowance governing this process.
//
// GOMAXPROCS has been cgroup-aware since Go 1.25, but the memory limit never
// was: a process in a 512 MiB container happily grows its heap past the cap and
// is SIGKILLed by the kernel instead of being pushed into a more aggressive GC
// cycle. Any workload whose peak working set approaches its container's cap hits
// exactly that failure mode.
//
// The limit set here is SOFT and covers all memory mapped, managed and not
// released by the Go runtime — the heap, but also goroutine stacks and runtime
// metadata. It excludes memory the runtime does not manage: the binary's own
// image, C allocations, syscall.Mmap mappings, and kernel memory held on the
// process's behalf. Subprocesses are outside it entirely.
//
// So this reduces OOM pressure rather than eliminating it. It is also the one
// direction the cgroup sibling does not cover: cgroup WRITES a control group to
// bound a child, memlimit READS the one already bounding this process.
package memlimit

import (
	"os"
	"runtime/debug"
	"strconv"
	"strings"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// cgroupV2LimitFile is the unified-hierarchy memory cap filename. Holds
// either a byte count or the literal "max".
const cgroupV2LimitFile string = "memory.max"

// cgroupV1LimitFile is the legacy hierarchy memory cap filename. Holds a byte
// count, using a near-int64-max sentinel to mean "unlimited".
const cgroupV1LimitFile string = "memory.limit_in_bytes"

// goMemLimitEnv is the runtime's own override. When an operator sets it, the
// runtime has already applied it and this package must not second-guess them.
const goMemLimitEnv string = "GOMEMLIMIT"

// unlimitedV2 is the cgroup v2 spelling of "no cap".
const unlimitedV2 string = "max"

// unlimitedV1Floor is the threshold above which a v1 value means "no cap".
// The kernel reports PAGE_COUNTER_MAX, which varies with page size, so any
// absurdly large value is treated as unlimited rather than matching one
// constant exactly.
const unlimitedV1Floor int64 = 1 << 62

// headroomPercent is the share of the cgroup allowance handed to the Go
// runtime. The remainder covers memory that counts against the cgroup but sits
// outside runtime accounting — the binary image, mapped files, C allocations,
// and kernel memory held on the process's behalf. Handing the runtime 100% of
// the cap would leave nothing for those and reintroduce the OOM kill this
// package prevents.
const headroomPercent int64 = 90

// percentDivisor converts headroomPercent into a fraction.
const percentDivisor int64 = 100

// decimalBase is the numeric base of every cgroup limit file.
const decimalBase int = 10

// int64Bits is the width parsePositiveBytes parses into, matching the int64
// that debug.SetMemoryLimit accepts.
const int64Bits int = 64

// minimumLimitBytes is the floor below which the derived limit is discarded.
// A cap this small cannot host a non-trivial working set anyway, and forcing
// the runtime under it would spin the collector continuously without averting
// the kill.
const minimumLimitBytes int64 = 64 << 20

// Apply derives the Go soft memory limit from the cgroup allowance and installs
// it, reporting what it read, what it derived, and what decided the outcome.
//
// The allowance is the tightest cap governing this process: resolved from
// /proc/self/cgroup so a child cgroup is honoured, then minimised across its
// ancestors and across both hierarchies on a hybrid host.
//
// It leaves the runtime default untouched — and says which of the three reasons
// applied — when the operator set GOMEMLIMIT to a non-empty value, when no
// cgroup file is readable (any non-Linux host) or none declares a cap, or when
// the derived limit is implausibly small.
func Apply() coreproc.MemoryLimitValue {
	//: Return the computed result to the caller.
	return applyFrom(os.LookupEnv, os.ReadFile, debug.SetMemoryLimit)
}

// applyFrom is Apply with its three environment dependencies injected so tests
// can drive every branch without a real cgroup filesystem.
func applyFrom(
	lookup func(string) (string, bool),
	readFile func(string) ([]byte, error),
	setLimit func(int64) int64,
) coreproc.MemoryLimitValue {
	//: An explicit GOMEMLIMIT is the operator's decision and already in
	//: effect; overriding it here would silently contradict them. An EMPTY
	//: value is not a decision — the Go runtime ignores it and stays
	//: unbounded, so treating mere presence as an override would disable
	//: this feature for an env var that does nothing.
	if value, isSet := lookup(goMemLimitEnv); isSet && strings.TrimSpace(value) != "" {
		//: Leave the runtime exactly as the operator configured it.
		return coreproc.MemoryLimitValue{Source: coreproc.MemorySourceOperator}
	}

	allowance, found := readCgroupAllowance(readFile)
	//: No readable cap: not containerised, or not Linux at all.
	if !found {
		//: Nothing to derive a limit from.
		return coreproc.MemoryLimitValue{Source: coreproc.MemorySourceUnconstrained}
	}

	derived := allowance / percentDivisor * headroomPercent
	//: Reject caps too small to host a real working set — capping the runtime
	//: below this would thrash the collector without averting the kill.
	if derived < minimumLimitBytes {
		//: Carry the allowance: the refusal only reads as one next to the cap
		//: that produced it.
		return coreproc.MemoryLimitValue{
			Allowance: allowance,
			Source:    coreproc.MemorySourceBelowFloor,
		}
	}

	setLimit(derived)

	//: Deliver the applied limit to the caller.
	return coreproc.MemoryLimitValue{
		Allowance: allowance,
		Limit:     derived,
		Source:    coreproc.MemorySourceCgroup,
	}
}

// readCgroupAllowance returns the most restrictive memory cap governing this
// process, in bytes. Reports false when no candidate declares a real cap.
//
// Takes the minimum across the process's own cgroup and its ancestors: a
// restrictive parent bounds the process just as effectively as its own cgroup,
// and honouring only the nearest value would overshoot the real allowance.
func readCgroupAllowance(readFile func(string) ([]byte, error)) (allowance int64, ok bool) {
	best := int64(0)
	//: Consult every candidate; the tightest cap is the one that binds.
	for _, candidate := range resolveCgroupPaths(readFile) {
		limit, found := readLimitFile(readFile, candidate)
		//: Absent files and unlimited caps contribute nothing.
		if !found {
			continue
		}
		//: Keep the smallest real cap seen so far.
		if best == 0 || limit < best {
			best = limit
		}
	}

	//: A zero best means no candidate declared a usable cap.
	return best, best > 0
}

// readLimitFile reads one cgroup limit file, dispatching on its filename to
// the matching format. v1 and v2 spell "unlimited" differently, so the parse
// cannot be shared.
func readLimitFile(readFile func(string) ([]byte, error), candidate string) (limit int64, ok bool) {
	content, err := readFile(candidate)
	//: An absent file simply means this hierarchy or level is not present.
	if err != nil {
		//: Signal absence to the caller.
		return 0, false
	}

	raw := string(content)
	//: v1 files carry the sentinel-based encoding.
	if strings.HasSuffix(candidate, cgroupV1LimitFile) {
		//: Return the computed result to the caller.
		return parseV1Limit(raw)
	}

	//: Return the computed result to the caller.
	return parseV2Limit(raw)
}

// parseV2Limit reads a cgroup v2 memory.max value, reporting false for the
// "max" sentinel and for anything unparseable or non-positive.
func parseV2Limit(raw string) (allowance int64, ok bool) {
	trimmed := strings.TrimSpace(raw)
	//: "max" is the v2 spelling of "no cap".
	if trimmed == unlimitedV2 {
		//: Signal absence of a cap.
		return 0, false
	}

	//: Return the computed result to the caller.
	return parsePositiveBytes(trimmed)
}

// parseV1Limit reads a cgroup v1 memory.limit_in_bytes value, treating the
// near-int64-max sentinel as unlimited.
func parseV1Limit(raw string) (allowance int64, ok bool) {
	parsed, valid := parsePositiveBytes(strings.TrimSpace(raw))
	//: Either an unparseable value or the huge "unlimited" sentinel — v1
	//: encodes no-cap as a number rather than a keyword — means there is no
	//: usable cap to derive a limit from.
	if !valid || parsed >= unlimitedV1Floor {
		//: Signal absence to the caller.
		return 0, false
	}

	//: Deliver the v1 allowance.
	return parsed, true
}

// parsePositiveBytes parses a decimal byte count, rejecting anything that is
// not a strictly positive integer.
func parsePositiveBytes(raw string) (allowance int64, ok bool) {
	parsed, err := strconv.ParseInt(raw, decimalBase, int64Bits)
	//: Malformed content means the file cannot inform the limit.
	if err != nil || parsed <= 0 {
		//: Signal absence to the caller.
		return 0, false
	}

	//: Deliver the parsed byte count.
	return parsed, true
}
