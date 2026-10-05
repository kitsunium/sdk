// Package snapshot provides Value[T any], a copy-on-write container for
// read-mostly shared state. Load is lock-free and allocation-free (a single
// atomic.Pointer load); writers (Store / Swap / Update) serialise on a mutex
// so a read-modify-write publish never loses a concurrent update. Stdlib-only
// and domain-neutral: any frozen-after-init or rarely-mutated table — a codec
// registry, a routing table, a feature-flag map, a hot-reloaded config — can
// reuse it.
//
// The core registries (internal/core/data/codec and the others) are built ON
// this primitive through kernel/plugin.Registry: the three atomic.Pointer[map]
// fields with hand-rolled CAS-loop writers the codec registry once carried are
// three tables of it. The container lives here, the map clone in plugin, and
// each domain's refusal with its registry (ADR 0011, ADR 0159).
package snapshot
