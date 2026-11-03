package cache

import (
	"context"
	"time"

	corecache "github.com/kitsunium/sdk/internal/core/data/cache"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// LoadRefreshing is Load with refresh-ahead (#838): a hit whose remaining
// lifetime is below before is returned at once, and ONE background fill per
// key replaces it while it is still valid. See [corecache.Refresher].
func (s *memoryStore[V]) LoadRefreshing(ctx context.Context, key string, fill corecache.Fill[V], before time.Duration) (value V, err error) {
	//: a window that is not positive has two readings — never, or always.
	if before <= 0 {
		var zero V
		//: refuse rather than pick one.
		return zero, refusedWindow(before)
	}
	stripe := s.stripeOf(key)
	stripe.mu.Lock()
	rec, ok := stripe.fetch(key)
	left := remainingOf(rec.expireAt, s.clk.Now())
	//: a live positive hit inside the window, with no refresh running yet:
	//: claim the key, under the same lock that read the entry.
	start := ok && rec.absent == nil && !rec.expireAt.IsZero() && left < before && stripe.claimRefresh(key)
	stripe.mu.Unlock()
	//: a miss fills on the caller's path, exactly as Load does.
	if !ok {
		//: the deduplicated fill.
		return s.Load(ctx, key, fill)
	}
	//: the refresh runs beside the answer, never before it.
	if start {
		go s.refresh(ctx, stripe, key, fill, left)
	}
	//: the cached answer — a value, or a negative entry's error.
	return rec.value, rec.absent
}

// claimRefresh records that a refresh of key is starting and reports whether
// none was running. Caller holds mu.
func (m *memoryStripe[V]) claimRefresh(key string) bool {
	//: one refresh per key at a time.
	if _, running := m.refreshing[key]; running {
		//: another hit already started it.
		return false
	}
	//: built on the first refresh this stripe ever runs.
	if m.refreshing == nil {
		m.refreshing = make(map[string]struct{})
	}
	m.refreshing[key] = struct{}{}
	//: this caller starts it.
	return true
}

// refresh is the background fill: on the caller's context values without its
// cancellation, bounded by the entry's remaining lifetime, its answer stored
// as Load stores one. A failure that is not an answer stores nothing: the old
// entry is served until it expires.
func (s *memoryStore[V]) refresh(ctx context.Context, stripe *memoryStripe[V], key string, fill corecache.Fill[V], left time.Duration) {
	//: the claim is released whatever the fill does, a panic included.
	defer stripe.releaseRefresh(key)
	//: the caller's values, its deadline replaced by the entry's own.
	bounded, cancel := context.WithTimeout(context.WithoutCancel(ctx), left)
	defer cancel()
	//: stored as Load stores. A failure leaves the old entry to expire: the
	//: caller was answered before the refresh began, so nobody is left to
	//: tell.
	if _, fillErr := s.fillAndStore(bounded, key, fill); fillErr != nil {
		//: the old entry serves until its deadline.
		return
	}
}

// releaseRefresh forgets the running refresh of key.
func (m *memoryStripe[V]) releaseRefresh(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	//: the next hit inside the window may start another.
	delete(m.refreshing, key)
}

// refusedWindow is the CACHE_MISCONFIGURED refusal of a refresh window.
func refusedWindow(before time.Duration) error {
	//: the option and the value, as a constructor's refusal names them.
	return kerrs.Wrap(corecache.CacheMisconfigured, kerrs.WrapParams{},
		kerrs.String("option", "before"), kerrs.String("value", before.String()))
}
