// Package plugin_test — what a dispatch pays to find its plug-in. Every
// registry in the core resolves a name on each call (codec.Lookup before a
// Marshal, crypto.LookupHasher before a Sum, …), so Lookup is the one path of
// this table that is hot, and it is measured beside the hand-written shape the
// six core registries carried before they became instances of Registry.
package plugin_test

import (
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/concur/snapshot"
	"github.com/kitsunium/sdk/internal/kernel/plugin"
)

// benchEntries is the size of the tables measured: the codec registry, the
// largest in the SDK, holds sixteen formats.
const benchEntries int = 16

// benchHit is a name every seeded table holds; benchMiss is one none holds.
const (
	benchHit  string = "plugin-07"
	benchMiss string = "absent"
)

// package-level sinks so the compiler cannot prove the lookups dead. The
// parallel bodies store into an atomic, because a plain variable written by
// every worker at once is a data race under -race.
var (
	lookupSink   identifier
	foundSink    bool
	namesSink    []string
	parallelSink atomic.Bool
)

// seededRegistry returns a Registry holding benchEntries plug-ins.
func seededRegistry() *plugin.Registry[string, identifier] {
	registry := new(plugin.Registry[string, identifier])
	for i := range benchEntries {
		registry.Publish(fmt.Sprintf("plugin-%02d", i), plug{id: i})
	}
	return registry
}

// handRolled is the shape the codec, writer, crypto, transform, id and view
// registries each wrote out before Registry: a snapshot of a map, loaded,
// checked for nil, and read.
type handRolled struct {
	table snapshot.Value[map[string]identifier]
}

// lookup is the hand-written read, line for line.
//
// IFACE-PLUGIN: it hands back the port the table stores, as every registry's
// Lookup does — the shape under measurement.
func (h *handRolled) lookup(name string) (value identifier, found bool) {
	current := h.table.Load()
	if current == nil {
		return nil, false
	}
	value, found = (*current)[name]
	return value, found
}

// seededHandRolled returns the hand-written table holding the same entries.
func seededHandRolled() *handRolled {
	entries := make(map[string]identifier, benchEntries)
	for i := range benchEntries {
		entries[fmt.Sprintf("plugin-%02d", i)] = plug{id: i}
	}
	h := new(handRolled)
	h.table.Store(&entries)
	return h
}

// BenchmarkLookup_Hit is the dispatch path: a name the table holds.
func BenchmarkLookup_Hit(b *testing.B) {
	registry := seededRegistry()
	b.ReportAllocs()
	for b.Loop() {
		lookupSink, foundSink = registry.Lookup(benchHit)
	}
}

// BenchmarkLookup_HitHandRolled is the same read through the hand-written
// shape the registries replaced — the baseline Lookup must not lose to.
func BenchmarkLookup_HitHandRolled(b *testing.B) {
	h := seededHandRolled()
	b.ReportAllocs()
	for b.Loop() {
		lookupSink, foundSink = h.lookup(benchHit)
	}
}

// BenchmarkLookup_Miss is a name nobody registered — a missing blank import.
func BenchmarkLookup_Miss(b *testing.B) {
	registry := seededRegistry()
	b.ReportAllocs()
	for b.Loop() {
		lookupSink, foundSink = registry.Lookup(benchMiss)
	}
}

// BenchmarkLookup_MissHandRolled is the miss through the hand-written shape.
func BenchmarkLookup_MissHandRolled(b *testing.B) {
	h := seededHandRolled()
	b.ReportAllocs()
	for b.Loop() {
		lookupSink, foundSink = h.lookup(benchMiss)
	}
}

// BenchmarkLookup_Parallel is the hit with every core dispatching at once: a
// lookup takes no lock, so it should scale rather than contend.
func BenchmarkLookup_Parallel(b *testing.B) {
	registry := seededRegistry()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		var found bool
		for pb.Next() {
			_, found = registry.Lookup(benchHit)
		}
		parallelSink.Store(found)
	})
}

// BenchmarkLookup_ParallelHandRolled is the parallel hit through the
// hand-written shape.
func BenchmarkLookup_ParallelHandRolled(b *testing.B) {
	h := seededHandRolled()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		var found bool
		for pb.Next() {
			_, found = h.lookup(benchHit)
		}
		parallelSink.Store(found)
	})
}

// BenchmarkNames is the listing an Available call makes: a sorted copy of the
// keys, the caller's own — one allocation, never on a dispatch path.
func BenchmarkNames(b *testing.B) {
	registry := seededRegistry()
	b.ReportAllocs()
	for b.Loop() {
		namesSink = registry.Names()
	}
}
