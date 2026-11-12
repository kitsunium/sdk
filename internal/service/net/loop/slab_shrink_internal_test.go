// Package loop — the slab gives a burst's arenas back, and its budget follows
// the cgroup allowance (#809).
package loop

import (
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// TestSlab_shrinkAfterBurst pins #809's shrink on the mapper double: a burst
// that maps 64 MiB over two classes and frees it falls back, within 100 ticks
// — a second of idle —, to one arena a class, each unmapped arena handed to
// unmap once, and the slab serves again from the arenas it kept or remapped.
func TestSlab_shrinkAfterBurst(t *testing.T) {
	t.Parallel()
	unmapped := 0
	s := newSlab(1<<20, 1<<30, heapMapper)
	s.unmap = func(b []byte) error { unmapped += len(b); return nil }
	var held []chunkRef
	for _, size := range []int{2 << 10, 64 << 10} {
		for range (32 << 20) / size {
			r, ok := s.alloc(size)
			if !ok {
				t.Fatal("alloc refused")
			}
			held = append(held, r)
		}
	}
	burst := s.mappedBytes
	if burst < 64<<20 {
		t.Fatalf("burst mapped %d, want 64 MiB", burst)
	}
	for _, r := range held {
		s.free(r)
	}
	for range 100 {
		s.shrink()
	}
	if want := 2 * arenaBytes; s.mappedBytes > want {
		t.Fatalf("mapped %d after 100 ticks of idle, want at most %d (one arena a class)", s.mappedBytes, want)
	}
	if unmapped != burst-s.mappedBytes {
		t.Fatalf("unmapped %d, want %d", unmapped, burst-s.mappedBytes)
	}
	for range 3 {
		held = held[:0]
		for range 1000 {
			r, ok := s.alloc(2 << 10)
			if !ok {
				t.Fatal("alloc refused after the shrink")
			}
			s.bytes(r)[0] = 1
			held = append(held, r)
		}
		for _, r := range held {
			s.free(r)
		}
		for range 100 {
			s.shrink()
		}
	}
	if s.inUse != 0 || s.mappedBytes > 2*arenaBytes {
		t.Fatalf("in use %d, mapped %d", s.inUse, s.mappedBytes)
	}
}

// TestSlab_shrinkKeepsBusyArenas pins the hysteresis: an arena with one chunk
// out stays, and one idle arena a class stays.
func TestSlab_shrinkKeepsBusyArenas(t *testing.T) {
	t.Parallel()
	s := newSlab(2<<10, 1<<30, heapMapper)
	var held []chunkRef
	for range 3 * (arenaBytes / (2 << 10)) {
		r, _ := s.alloc(2 << 10)
		held = append(held, r)
	}
	for i, r := range held {
		if i != 0 {
			s.free(r)
		}
	}
	for range 10 {
		s.shrink()
	}
	if s.mappedBytes != 2*arenaBytes {
		t.Fatalf("mapped %d, want the busy arena and one idle", s.mappedBytes)
	}
	if s.bytes(held[0])[0] = 7; s.bytes(held[0])[0] != 7 {
		t.Fatal("the busy chunk moved")
	}
	if s.reclaimable() {
		t.Fatal("reclaimable with one idle arena a class: an idle loop would wake for nothing")
	}
}

// TestSlab_reclaimable pins what keeps a loop with nothing timed on the tick
// (#809): two idle arenas in a class are something to give back, a busy
// arena or one idle arena is not, and the shrink ends it.
func TestSlab_reclaimable(t *testing.T) {
	t.Parallel()
	s := newSlab(2<<10, 1<<30, heapMapper)
	if s.reclaimable() {
		t.Fatal("an empty slab is reclaimable")
	}
	var held []chunkRef
	for range 3 * (arenaBytes / (2 << 10)) {
		r, _ := s.alloc(2 << 10)
		held = append(held, r)
	}
	if s.reclaimable() {
		t.Fatal("reclaimable with every chunk out")
	}
	for _, r := range held {
		s.free(r)
	}
	if !s.reclaimable() {
		t.Fatal("three idle arenas are not reclaimable")
	}
	for tick := 0; s.reclaimable(); tick++ {
		if tick == 10 {
			t.Fatal("still reclaimable after 10 ticks")
		}
		s.shrink()
	}
	if s.mappedBytes != arenaBytes {
		t.Fatalf("mapped %d once nothing is reclaimable, want one arena", s.mappedBytes)
	}
}

// TestSlabBudgetFor pins #809's derivation: a cgroup allowance of 512 MiB and
// four loops is 32 MiB a loop; none is 128 MiB; the caller's own wins; the
// derived budget is never under one connection's largest chunk nor over the
// default.
func TestSlabBudgetFor(t *testing.T) {
	t.Parallel()
	mib := 1 << 20
	for _, c := range []struct {
		name     string
		explicit int
		allow    int64
		loops    int
		want     int
		src      string
	}{
		{"512 MiB, 4 loops", 0, 512 << 20, 4, 32 * mib, slabDerived},
		{"no allowance", 0, 0, 4, defaultSlabBudget, slabDefault},
		{"explicit", 7 * mib, 512 << 20, 4, 7 * mib, slabExplicit},
		{"a large allowance", 0, 64 << 30, 2, defaultSlabBudget, slabDerived},
		{"a tiny allowance", 0, 8 << 20, 8, mib, slabDerived},
	} {
		got, src := slabBudgetFor(c.explicit, coreproc.MemoryLimitValue{Allowance: c.allow}, c.loops, mib)
		if got != c.want || src != c.src {
			t.Errorf("%s: %d %s, want %d %s", c.name, got, src, c.want, c.src)
		}
	}
}
