// Package memlimit — resolving WHICH cgroup limit files govern this process:
// where each hierarchy is mounted, which part of the filesystem that mount
// exposes, which cgroup the process belongs to, and the ancestors whose caps
// bound it just as effectively.
//
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
