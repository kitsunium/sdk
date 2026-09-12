//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/memlimit .

// Package memlimit derives the Go runtime soft memory limit from the control
// group allowance governing this process, and installs it.
//
// GOMAXPROCS has been cgroup-aware since Go 1.25; the memory limit never was. A
// process in a 512 MiB container grows its heap past the cap and is SIGKILLed by
// the kernel instead of being pushed into a more aggressive GC cycle. One call at
// start-up converts that kill into collector pressure.
//
// It is the thin public facade over internal/service/proc/memlimit: the types are
// aliases of the core proc value types and the function delegates straight to the
// service implementation.
//
// # Usage
//
// Apply it once, as early as possible, before the heap has grown:
//
//	import (
//		"github.com/kitsunium/sdk/pkg/v1/memlimit"
//	)
//
//	func main() {
//		applied := memlimit.Apply()
//		if applied.Applied() {
//			// applied.Limit bytes, derived from applied.Allowance.
//		}
//	}
//
// Apply never fails and never panics: every outcome is a value. A caller that
// only wants to know whether it acted reads Applied; one that wants to log WHY it
// did not reads Source, which distinguishes an operator-set GOMEMLIMIT from an
// uncapped host from a cap too tight to honour.
//
// # What the limit covers
//
// The limit is SOFT and covers all memory mapped, managed and not released by the
// Go runtime — the heap, goroutine stacks, runtime metadata. It excludes memory
// the runtime does not manage: the binary image, C allocations, syscall.Mmap
// mappings, and kernel memory held on the process's behalf. Subprocesses are
// outside it entirely. Ninety percent of the cgroup allowance is handed to the
// runtime and the remainder covers those, so this reduces OOM pressure rather
// than eliminating it.
//
// # Relation to cgroup and rlimit
//
// The three are distinct directions on one subject. cgroup WRITES a control group
// to bound a child process. rlimit applies a setrlimit(2) ceiling the kernel
// enforces by failing allocations. memlimit READS the cgroup cap already bounding
// THIS process and tunes the Go collector to live inside it — nothing is enforced,
// the collector simply works harder as the heap approaches the limit.
//
// # Platform notes
//
// Control groups are a Linux facility. Off Linux no limit file is readable, so
// Apply reports MemorySourceUnconstrained and leaves the runtime default in
// place; no call fails and no build tag is needed.
package memlimit

import (
	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	svcmemlimit "github.com/kitsunium/sdk/internal/service/proc/memlimit"
)

// MemorySourceOperator reports that GOMEMLIMIT carried a non-empty value, which
// the runtime has already applied. It re-exports the core enum value so consumers
// qualify it as memlimit.MemorySourceOperator.
const MemorySourceOperator Source = coreproc.MemorySourceOperator

// MemorySourceUnconstrained reports that no control group governing this process
// declares a memory cap.
const MemorySourceUnconstrained Source = coreproc.MemorySourceUnconstrained

// MemorySourceBelowFloor reports that a cap was found but the limit derived from
// it fell under the usable floor and was discarded.
const MemorySourceBelowFloor Source = coreproc.MemorySourceBelowFloor

// MemorySourceCgroup reports the one outcome that yields a limit: a real cap was
// read from the control-group hierarchy and the derived value was applied.
const MemorySourceCgroup Source = coreproc.MemorySourceCgroup

// Source names what decided the soft memory limit. It aliases the core proc type.
type Source = coreproc.MemorySource

// Limit is an immutable record of one derivation: the allowance read, the limit
// derived, and the source that decided it. It aliases the core proc type.
type Limit = coreproc.MemoryLimitValue

// Apply derives the Go soft memory limit from the tightest control-group cap
// governing this process and installs it, returning what it read, what it
// derived, and what decided the outcome.
//
// It stands aside — leaving the runtime default in place — when GOMEMLIMIT is
// already set, when no cgroup declares a cap, and when the derived limit is too
// small to be useful. Those three are told apart by Limit.Source.
func Apply() Limit {
	//: delegate verbatim to the service implementation.
	return svcmemlimit.Apply()
}
