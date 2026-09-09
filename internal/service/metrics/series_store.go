// Package metrics — one instrument kind's series map.
package metrics

import coremetrics "github.com/kitsunium/sdk/internal/core/metrics"

// seriesStore holds every live series of ONE instrument kind, keyed by the
// canonical series key (name + sorted label set, see series.go).
//
// Flat, not nested by name: one map read resolves a labelled fetch, where a
// name→labels nesting would cost two on the path every observation walks. The
// kind travels with the store rather than being passed to each call.
type seriesStore[T any] struct {
	kind  instrumentKind
	byKey map[string]*seriesEntry[T]
}

// newSeriesStore returns an empty store for kind.
func newSeriesStore[T any](kind instrumentKind) seriesStore[T] {
	//: no capacity hint — an instrument count is not knowable at construction.
	return seriesStore[T]{kind: kind, byKey: make(map[string]*seriesEntry[T], 0)}
}

// lookup resolves an EXISTING series under the meter's read lock.
//
// key is []byte rather than a string on purpose: `m[string(b)]` compiles to a
// lookup that does not allocate, while converting first costs one allocation on
// a path that runs once per observation.
func (s *seriesStore[T]) lookup(meter *memMeter, key []byte) (inst T, ok bool) {
	meter.mu.RLock()
	entry, found := s.byKey[string(key)]
	meter.mu.RUnlock()
	//: absence is the caller's cue to take the write lock.
	if !found {
		//: zero value; the caller ignores it.
		return inst, false
	}
	//: the overwhelmingly common case.
	return entry.inst, true
}

// admit resolves — or creates — the series for (name, key, sorted) under the
// meter's WRITE lock.
//
// Once the name has reached its cardinality bound, a NEW label set is not
// rejected and not dropped: it is folded into the name's single overflow
// series. That choice and its cost:
//
//   - Memory is bounded. This is the whole point: an unbounded label set is a
//     process-killing leak, not a reporting inconvenience.
//   - Nothing is silently lost. A counter's grand total across all its series
//     stays correct, because every increment still lands somewhere.
//   - The breakdown IS lost, irrecoverably. Once folded, an observation's
//     labels are gone; no downstream aggregation can recover which label set
//     it came from.
//   - Which series keep their identity is ARRIVAL-ORDER dependent. The first
//     MaxSeriesPerInstrument label sets seen win, so two replicas of the same
//     service can fold different label sets into overflow and disagree about
//     what is visible. That is the real cost of not evicting.
//   - The condition is VISIBLE. A series carrying sdk_metric_overflow="true"
//     appears in every snapshot from then on, so an operator reading a
//     dashboard learns their labels blew up. A typed error cannot be returned
//     from an accessor whose signature hands back a Counter without changing
//     every call site, and dropping the observation would be exactly the inert
//     behaviour ADR 0031 exists to ban.
//
// key stays []byte here too: an instrument already in overflow misses the read
// lock on EVERY observation and lands in this function every time, so a string
// conversion on entry would trade the memory leak the bound prevents for GC
// pressure with the same cause. The conversion happens only on insertion,
// where the key is actually retained.
func (s *seriesStore[T]) admit(
	meter *memMeter, name string, key []byte, sorted []coremetrics.LabelValue, build func() T,
) T {
	meter.mu.Lock()
	defer meter.mu.Unlock()
	//: another goroutine may have created it between the read unlock and here.
	if entry, ok := s.byKey[string(key)]; ok {
		//: the raced creation wins; both callers get the one instrument.
		return entry.inst
	}
	//: bind (or verify) the name's kind before spending a series slot on it.
	state := meter.bindName(name, s.kind)
	//: at the bound, the new label set folds into the overflow series.
	if state.series >= meter.maxSeries {
		//: retarget this creation at the overflow identity.
		return s.overflow(state, name, build)
	}
	//: an admitted label set spends one slot of the bound.
	state.series++
	//: create, own the labels, publish under the retained key.
	entry := &seriesEntry[T]{name: name, labels: cloneLabels(sorted), inst: build()}
	s.byKey[retain(key)] = entry
	//: hand back the fresh instrument.
	return entry.inst
}

// retain copies key into an owned string, which is the ONE place in the fetch
// path a series key is meant to allocate: a map key outlives the caller's stack
// scratch, so it has to be copied. Every other use of the key indexes a map
// with string(bytes) directly, which the compiler turns into a lookup that
// allocates nothing.
func retain(key []byte) string {
	//: the map keeps this string for the life of the series.
	return string(key)
}

// overflow resolves — or creates once — the aggregated overflow series of name.
// Caller holds the write lock and has already established that the bound is
// reached.
func (s *seriesStore[T]) overflow(state *nameState, name string, build func() T) T {
	//: the key is computed once per name and cached on the state.
	key, labels := state.overflowSeries(name)
	//: after the first fold every further label set lands in this series.
	if entry, ok := s.byKey[key]; ok {
		//: one instrument absorbs the whole tail.
		return entry.inst
	}
	//: first fold — create the aggregated series.
	entry := &seriesEntry[T]{name: name, labels: cloneLabels(labels), inst: build()}
	s.byKey[key] = entry
	//: hand back the series every later overflow will reuse.
	return entry.inst
}
