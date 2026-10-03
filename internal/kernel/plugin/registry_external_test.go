// Package plugin_test — Registry from the outside: the three outcomes of a
// publish, the holder a claim reports, the two answers of a lookup, the order
// of Names, and the races a copy-on-write table exists to win.
package plugin_test

import (
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/plugin"
)

// writers is how many goroutines publish at once in the race test.
const writers int = 32

// identifier is the interface a registry stores — a port, the shape every SDK
// registrar keeps its plug-ins behind, with an identity and a rank.
type identifier interface {
	ID() int
	Rank() int
}

// plug is a comparable implementation; two plugs are equal when their ids are.
type plug struct{ id int }

// ID identifies the plug.
func (p plug) ID() int { return p.id }

// Rank orders the plug; only its identity matters to a registry.
func (p plug) Rank() int { return -p.id }

// TestPublishHasThreeOutcomes pins what a registrar turns into its own panic:
// a fresh name publishes, the IDENTICAL value republishes as a no-op — a
// plug-in package imported through two paths registers twice — and a DIFFERENT
// value under a taken name is the conflict, which leaves the first in place.
func TestPublishHasThreeOutcomes(t *testing.T) {
	t.Parallel()
	type tc struct {
		name         string
		first        identifier
		second       identifier
		wantConflict bool
	}
	tests := []tc{
		{name: "a fresh name publishes", first: plug{id: 1}},
		{name: "the identical value republishes as a no-op", first: plug{id: 1}, second: plug{id: 1}},
		{name: "a different value conflicts", first: plug{id: 1}, second: plug{id: 2}, wantConflict: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var registry plugin.Registry[string, identifier]
		if registry.Publish("key", c.first) {
			t.Fatal("the first publish of a fresh name reported a conflict")
		}
		//: a single-publish case has only resolution left to assert.
		if c.second != nil {
			if got := registry.Publish("key", c.second); got != c.wantConflict {
				t.Fatalf("the second publish reported conflict=%v, want %v", got, c.wantConflict)
			}
		}
		//: whatever happened, the FIRST registration still resolves — a
		//: refused publish may not disturb what was already there.
		got, found := registry.Lookup("key")
		if !found || got != c.first {
			t.Errorf("Lookup = %v, %v; want the first-registered %v", got, found, c.first)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestClaimReportsTheHolder pins what Claim answers where Publish answers a
// verdict: a free name is taken by the claim, and a taken one — by the
// identical value or by another — is reported with the value holding it and
// left as it was. The identical value reported as taken is the property a
// registrar that refuses every second registration relies on; the holder is
// what a refusal that names the owner reads.
func TestClaimReportsTheHolder(t *testing.T) {
	t.Parallel()
	type tc struct {
		name      string
		first     identifier
		second    identifier
		wantTaken bool
	}
	tests := []tc{
		{name: "a free name is claimed", first: plug{id: 1}},
		{name: "the identical value is reported as taken", first: plug{id: 1}, second: plug{id: 1}, wantTaken: true},
		{name: "a different value is reported as taken", first: plug{id: 1}, second: plug{id: 2}, wantTaken: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var registry plugin.Registry[string, identifier]
		if holder, taken := registry.Claim("key", c.first); taken || holder != nil {
			t.Fatalf("the first claim of a free name = %v, %v; want nil, false", holder, taken)
		}
		//: a single-claim case has only resolution left to assert.
		if c.second != nil {
			holder, taken := registry.Claim("key", c.second)
			if taken != c.wantTaken || holder != c.first {
				t.Fatalf("the second claim = %v, %v; want the first %v, %v", holder, taken, c.first, c.wantTaken)
			}
		}
		//: whatever happened, the FIRST claim still resolves.
		got, found := registry.Lookup("key")
		if !found || got != c.first {
			t.Errorf("Lookup = %v, %v; want the first-claimed %v", got, found, c.first)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestLookupMissesCleanly pins the two misses: before anything is published,
// and on a name nobody published. Both hand back the zero value AND false, so a
// caller checking only the value of an interface-typed V holds a nil it can
// still tell apart from a hit.
func TestLookupMissesCleanly(t *testing.T) {
	t.Parallel()
	var registry plugin.Registry[string, identifier]
	if got, found := registry.Lookup("absent"); found || got != nil {
		t.Errorf("Lookup on an empty registry = %v, %v; want nil, false", got, found)
	}
	registry.Publish("present", plug{id: 7})
	if got, found := registry.Lookup("absent"); found || got != nil {
		t.Errorf("Lookup of an unpublished name = %v, %v; want nil, false", got, found)
	}
}

// TestNamesAreSortedAndOwned pins the order Names answers in — ascending, never
// map order — the nil it answers before the first publish, and that the slice
// is the caller's: sorting or overwriting it cannot reach the table.
func TestNamesAreSortedAndOwned(t *testing.T) {
	t.Parallel()
	var registry plugin.Registry[string, identifier]
	if names := registry.Names(); names != nil {
		t.Errorf("Names on an empty registry = %v, want nil", names)
	}
	for i, name := range []string{"zeta", "alpha", "mu"} {
		registry.Publish(name, plug{id: i})
	}
	names := registry.Names()
	if want := []string{"alpha", "mu", "zeta"}; !slices.Equal(names, want) {
		t.Fatalf("Names = %v, want %v", names, want)
	}
	//: the caller's slice is the caller's.
	names[0] = "overwritten"
	if again := registry.Names(); again[0] != "alpha" {
		t.Errorf("overwriting the returned slice reached the table: %v", again)
	}
}

// TestConcurrentPublishesLoseNothing pins the property the copy-on-write table
// exists for: writers racing each other while readers read, and every entry
// lands. A writer that copied a stale snapshot and published it over another's
// would drop an entry; a writer editing the live map would trip the race
// detector on the readers.
func TestConcurrentPublishesLoseNothing(t *testing.T) {
	t.Parallel()
	var registry plugin.Registry[string, identifier]
	var wg sync.WaitGroup
	for i := range writers {
		wg.Go(func() {
			registry.Publish(fmt.Sprintf("plug-%02d", i), plug{id: i})
		})
		wg.Go(func() {
			//: a reader in the middle of the writes.
			registry.Lookup(fmt.Sprintf("plug-%02d", i))
			registry.Names()
		})
	}
	wg.Wait()
	if got := len(registry.Names()); got != writers {
		t.Fatalf("%d entries survived %d concurrent publishes", got, writers)
	}
	for i := range writers {
		if got, found := registry.Lookup(fmt.Sprintf("plug-%02d", i)); !found || got.ID() != i {
			t.Errorf("plug-%02d resolved to %v, %v", i, got, found)
		}
	}
}

// TestConcurrentClaimsHaveOneWinner pins that the check and the insert are one
// step: writers claiming ONE name with different values at once, and exactly
// one of them takes it, while every other is told it is taken and by the
// winner's value. A check made before the lock, or a holder read by a second
// lookup, would let two writers both believe they won.
func TestConcurrentClaimsHaveOneWinner(t *testing.T) {
	t.Parallel()
	var registry plugin.Registry[string, identifier]
	holders := make([]identifier, writers)
	takens := make([]bool, writers)
	var wg sync.WaitGroup
	for i := range writers {
		wg.Go(func() {
			holders[i], takens[i] = registry.Claim("contested", plug{id: i})
		})
	}
	wg.Wait()
	winner, found := registry.Lookup("contested")
	if !found {
		t.Fatal("no claim took the contested name")
	}
	var won int
	for i := range writers {
		//: the one claim that found the name free.
		if !takens[i] {
			won++
			if winner.ID() != i {
				t.Errorf("claim %d took the name but %v holds it", i, winner)
			}
			continue
		}
		//: every other claim names the winner as the holder.
		if holders[i] != winner {
			t.Errorf("claim %d was told %v holds the name, but %v does", i, holders[i], winner)
		}
	}
	if won != 1 {
		t.Fatalf("%d claims took the contested name, want exactly 1", won)
	}
}
