package cache_test

import (
	"context"
	"sync"
	"testing"

	corecache "github.com/kitsunium/sdk/internal/core/data/cache"
	svccache "github.com/kitsunium/sdk/internal/service/data/cache"
)

// gate pauses one call of a gatedTier: reached is closed when the call gets
// there, and the call waits for release. A gate fires once.
type gate struct {
	reached chan struct{}
	release chan struct{}
	once    sync.Once
}

// newGate returns an armed gate.
func newGate() *gate {
	return &gate{reached: make(chan struct{}), release: make(chan struct{})}
}

// pass announces the call and holds it until the test releases it.
func (g *gate) pass() {
	if g == nil {
		return
	}
	g.once.Do(func() {
		close(g.reached)
		<-g.release
	})
}

// gatedTier is a full tier whose FetchEntry can pause AFTER it has read the
// entry and BEFORE it returns it — the window a remote tier's round trip
// opens — and whose Set can pause before it writes, so a test can place a
// write exactly inside a promotion, with no sleep and no retry.
type gatedTier struct {
	inner corecache.Store[string]
	mu    sync.Mutex
	fetch *gate
	set   *gate
}

// newGatedTier returns a gatedTier over a fresh memory store.
func newGatedTier(t *testing.T) *gatedTier {
	t.Helper()
	inner, err := svccache.NewMemory[string](svccache.MemoryConfig{MaxEntries: 16})
	if err != nil {
		t.Fatal(err)
	}
	return &gatedTier{inner: inner}
}

// armFetch pauses the next FetchEntry after its read.
func (g *gatedTier) armFetch() *gate {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.fetch = newGate()
	return g.fetch
}

// armSet pauses the next Set before its write.
func (g *gatedTier) armSet() *gate {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.set = newGate()
	return g.set
}

// take returns the armed gate in *slot and disarms it.
func (g *gatedTier) take(slot **gate) *gate {
	g.mu.Lock()
	defer g.mu.Unlock()
	armed := *slot
	*slot = nil
	return armed
}

func (g *gatedTier) Fetch(ctx context.Context, k string) (string, bool, error) {
	return g.inner.Fetch(ctx, k)
}

func (g *gatedTier) Set(ctx context.Context, k string, e corecache.EntryValue[string]) error {
	g.take(&g.set).pass()
	return g.inner.Set(ctx, k, e)
}

func (g *gatedTier) Delete(ctx context.Context, k string) error { return g.inner.Delete(ctx, k) }

func (g *gatedTier) FetchEntry(ctx context.Context, k string) (corecache.EntryValue[string], bool, error) {
	e, ok, err := g.inner.(corecache.EntryFetcher[string]).FetchEntry(ctx, k)
	g.take(&g.fetch).pass()
	return e, ok, err
}

func (g *gatedTier) InvalidateTag(ctx context.Context, tag string) (int, error) {
	return g.inner.(corecache.Tagger).InvalidateTag(ctx, tag)
}

// raceChain is a chain of tiers, nearest first, each a gatedTier.
func raceChain(t *testing.T, tiers int) (corecache.Store[string], []*gatedTier) {
	t.Helper()
	gated := make([]*gatedTier, tiers)
	stores := make([]corecache.Store[string], tiers)
	for i := range gated {
		gated[i] = newGatedTier(t)
		stores[i] = gated[i]
	}
	chain, err := svccache.NewChain[string](svccache.ChainConfig{}, stores...)
	if err != nil {
		t.Fatal(err)
	}
	return chain, gated
}

// forever is an entry with no deadline: a stale promotion of it would be
// served until evicted.
func forever(value string, tags ...string) corecache.EntryValue[string] {
	return corecache.EntryValue[string]{Value: value, TTL: corecache.NoExpiry, Tags: tags}
}

// inBackground runs fn on its own goroutine and returns a channel closed when
// it has returned.
func inBackground(fn func()) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	return done
}

// heldInTier reports what tier holds under key, reading it directly.
func heldInTier(t *testing.T, tier *gatedTier, key string) (string, bool) {
	t.Helper()
	v, ok, err := tier.Fetch(context.Background(), key)
	if err != nil {
		t.Fatalf("tier Fetch(%q) = %v", key, err)
	}
	return v, ok
}

// TestAPromotionNeverUndoesAWriteThatRanDuringItsFarRead pins #460: a Fetch
// reads v1 in the far tier, and a write that runs to completion on every tier
// before the promotion starts must not be overwritten by v1 in a nearer tier
// — on a chain of two tiers and of three, for a Delete, a Set, and an
// InvalidateTag, through Fetch and through Load.
func TestAPromotionNeverUndoesAWriteThatRanDuringItsFarRead(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	writes := map[string]struct {
		write func(chain corecache.Store[string]) error
		want  string // "" means the key must be gone
	}{
		"delete": {write: func(c corecache.Store[string]) error { return c.Delete(ctx, "k") }},
		"set":    {write: func(c corecache.Store[string]) error { return c.Set(ctx, "k", forever("new")) }, want: "new"},
		"invalidate": {write: func(c corecache.Store[string]) error {
			_, err := c.(corecache.Tagger).InvalidateTag(ctx, "t")
			return err
		}},
	}
	reads := map[string]func(chain corecache.Store[string]){
		"fetch": func(c corecache.Store[string]) { _, _, _ = c.Fetch(ctx, "k") },
		"load": func(c corecache.Store[string]) {
			_, _ = c.(corecache.Loader[string]).Load(ctx, "k", func(context.Context) (corecache.EntryValue[string], error) {
				return forever("filled"), nil
			})
		},
	}
	for _, tiers := range []int{2, 3} {
		for writeName, w := range writes {
			for readName, read := range reads {
				t.Run(writeName+"/"+readName+"/"+string(rune('0'+tiers))+"tiers", func(t *testing.T) {
					t.Parallel()
					chain, gated := raceChain(t, tiers)
					far := gated[tiers-1]
					if err := far.Set(ctx, "k", forever("v1", "t")); err != nil {
						t.Fatal(err)
					}
					paused := far.armFetch()
					done := inBackground(func() { read(chain) })
					<-paused.reached
					if err := w.write(chain); err != nil {
						t.Fatalf("the write = %v", err)
					}
					close(paused.release)
					<-done
					for i, tier := range gated[:tiers-1] {
						if v, ok := heldInTier(t, tier, "k"); ok && v != w.want {
							t.Fatalf("tier %d holds %q after the write returned; want %q", i, v, w.want)
						}
					}
					v, ok, err := chain.Fetch(ctx, "k")
					if err != nil {
						t.Fatal(err)
					}
					if w.want == "" && ok {
						t.Fatalf("the chain serves %q after the write removed it", v)
					}
					if w.want != "" && v != w.want {
						t.Fatalf("the chain serves %q, want %q", v, w.want)
					}
				})
			}
		}
	}
}

// TestAWriteWaitsForAPromotionAlreadyWriting pins the other half: a promotion
// past its check, paused inside its near write, is finished before a Delete
// touches a tier, so the Delete removes the promoted copy rather than being
// overwritten by it.
func TestAWriteWaitsForAPromotionAlreadyWriting(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	chain, gated := raceChain(t, 2)
	near, far := gated[0], gated[1]
	if err := far.Set(ctx, "k", forever("v1")); err != nil {
		t.Fatal(err)
	}
	writing := near.armSet()
	fetched := inBackground(func() { _, _, _ = chain.Fetch(ctx, "k") })
	<-writing.reached
	deleted := inBackground(func() {
		if err := chain.Delete(ctx, "k"); err != nil {
			t.Errorf("Delete() = %v", err)
		}
	})
	close(writing.release)
	<-fetched
	<-deleted
	if v, ok := heldInTier(t, near, "k"); ok {
		t.Fatalf("the near tier holds %q after the Delete returned", v)
	}
	if v, ok, _ := chain.Fetch(ctx, "k"); ok {
		t.Fatalf("the chain serves %q after the Delete returned", v)
	}
}

// TestAPromotionWithNoWriteAroundItStillPromotes pins that the guard costs no
// promotion when nothing writes: a far hit is copied into every nearer tier,
// and a second read is answered by the nearest.
func TestAPromotionWithNoWriteAroundItStillPromotes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	chain, gated := raceChain(t, 3)
	if err := chain.Set(ctx, "other", forever("x")); err != nil {
		t.Fatal(err)
	}
	if err := gated[2].Set(ctx, "k", forever("v1")); err != nil {
		t.Fatal(err)
	}
	if v, ok, err := chain.Fetch(ctx, "k"); err != nil || !ok || v != "v1" {
		t.Fatalf("Fetch() = %q, %v, %v", v, ok, err)
	}
	for i, tier := range gated[:2] {
		if v, ok := heldInTier(t, tier, "k"); !ok || v != "v1" {
			t.Fatalf("tier %d holds %q, %v after a promotion; want v1", i, v, ok)
		}
	}
}
