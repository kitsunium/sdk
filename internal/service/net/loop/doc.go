// Package loop is the network domain's event-loop engine (ADR 0186): the
// native backends a plaintext stream group runs on when it is served by a
// corenet.LoopHandler — epoll on every Linux architecture, io_uring after a
// staged probe of the running kernel — and nothing else of the SDK.
//
// It is a leaf below server (§D3): it imports internal/core/net, the shared
// UnsupportedPlatform of internal/core/proc and the kernel, never server.
// server binds or adopts the listeners, keeps the lifecycle, the admission and
// the drain, and hands the loop duplicated descriptors through one type,
// Listener. The portable backend stays in server: it needs the netpoller.
//
// A loop is one goroutine locked to its OS thread, owning its rings or its
// epoll instance. Its connection table is pointer-free Go memory the race
// detector sees; every byte the kernel reads or writes — the read arena, the
// spill and output slab — is mapped outside the Go heap (§D7). The engine
// knows bytes, references, deadlines, write pressure and the order of
// writes; it speaks no HTTP.
//
// An idle connection on a native backend costs at most 256 bytes of Go heap
// — its slot and its timer link — and no mapped byte: its output chunk is
// attached at its first Write and returned once drained.
package loop
