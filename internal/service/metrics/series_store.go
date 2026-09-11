// Package metrics — one snapshot group's series map.
package metrics

import coremetrics "github.com/kitsunium/sdk/internal/core/metrics"

// seriesStore holds every live series of ONE snapshot group (sums, gauges or
// histograms), keyed by the canonical series key (name + sorted attribute set,
// see series.go).
//
// Flat, not nested by name: one map read resolves an attributed fetch, where a
// name→attrs nesting would cost two on the path every observation walks.
//
// The store is keyed on the GROUP rather than on the instrument kind because a
// Counter and an UpDownCounter produce the same point shape and land in the
// same snapshot map; which of the two a name is bound to lives on its
// nameState, where it is decided once instead of per series.
//
// meter is a back-pointer to the meter that owns the store. It is a FIELD
// rather than a sixth parameter on admit for a measured reason: Go's escape
// analysis is field-INSENSITIVE on a struct parameter, so grouping the series'
// identity (name, kind, key, attrs) into one value to shorten the signature
// makes the whole group escape the moment the name is stored on an entry —
// which drags the caller's stack scratch onto the heap and costs two
// allocations per observation. Moving the meter out of the signature keeps the
// four identity arguments separate and the fetch path allocation-free.
type seriesStore[T any] struct {
	meter *memMeter
	byKey map[string]*seriesEntry[T]
}

// newSeriesStore returns an empty store bound to the meter that owns it.
func newSeriesStore[T any](meter *memMeter) seriesStore[T] {
	//: no capacity hint — an instrument count is not knowable at construction.
	return seriesStore[T]{meter: meter, byKey: make(map[string]*seriesEntry[T], 0)}
}

// lookup resolves an EXISTING series under the meter's read lock.
//
// key is []byte rather than a string on purpose: `m[string(b)]` compiles to a
// lookup that does not allocate, while converting first costs one allocation on
// a path that runs once per observation.
func (s *seriesStore[T]) lookup(key []byte) (inst T, ok bool) {
	s.meter.mu.RLock()
	entry, found := s.byKey[string(key)]
	s.meter.mu.RUnlock()
	//: absence is the caller's cue to take the write lock.
	if !found {
		//: zero value; the caller ignores it.
		return inst, false
	}
	//: the overwhelmingly common case.
	return entry.inst, true
}

// admit resolves — or creates — the series for (name, kind, key, sorted) under
// the meter's WRITE lock.
//
// Once the name has reached its cardinality bound, a NEW attribute set is not
// rejected and not dropped: it is folded into the name's single overflow
// series. That choice and its cost:
//
//   - Memory is bounded. This is the whole point: an unbounded attribute set is
//     a process-killing leak, not a reporting inconvenience.
//   - Nothing is silently lost. A counter's grand total across all its series
//     stays correct, because every increment still lands somewhere.
//   - The breakdown IS lost, irrecoverably. Once folded, an observation's
//     attributes are gone; no downstream aggregation can recover which set it
//     came from.
//   - Which series keep their identity is ARRIVAL-ORDER dependent. The first
//     MaxSeriesPerInstrument attribute sets seen win, so two replicas of the
//     same service can fold different sets into overflow and disagree about
//     what is visible. That is the real cost of not evicting.
//   - The condition is VISIBLE. A series carrying sdk_metric_overflow=true
//     appears in every snapshot from then on, so an operator reading a
//     dashboard learns their attributes blew up. A typed error cannot be
//     returned from an accessor whose signature hands back a Counter without
//     changing every call site, and dropping the observation would be exactly
//     the inert behaviour ADR 0031 exists to ban.
//
// key stays []byte here too: an instrument already in overflow misses the read
// lock on EVERY observation and lands in this function every time, so a string
// conversion on entry would trade the memory leak the bound prevents for GC
// pressure with the same cause. The conversion happens only on insertion,
// where the key is actually retained.
func (s *seriesStore[T]) admit(
	name string, kind instrumentKind, key []byte, sorted []coremetrics.AttrValue, build func() T,
) T {
	s.meter.mu.Lock()
	defer s.meter.mu.Unlock()
	//: another goroutine may have created it between the read unlock and here.
	if entry, ok := s.byKey[string(key)]; ok {
		//: the raced creation wins; both callers get the one instrument.
		return entry.inst
	}
	//: bind (or verify) the name's kind before spending a series slot on it.
	state := s.meter.bindName(name, kind)
	//: at the bound, the new attribute set folds into the overflow series.
	if state.series >= s.meter.maxSeries {
		//: retarget this creation at the overflow identity.
		return s.overflow(state, name, build)
	}
	//: an admitted attribute set spends one slot of the bound.
	state.series++
	//: create, own the attributes, publish under the retained key.
	entry := &seriesEntry[T]{name: name, attrs: cloneAttrs(sorted), inst: build()}
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
	key, attrs := state.overflowSeries(name)
	//: after the first fold every further attribute set lands in this series.
	if entry, ok := s.byKey[key]; ok {
		//: one instrument absorbs the whole tail.
		return entry.inst
	}
	//: first fold — create the aggregated series.
	entry := &seriesEntry[T]{name: name, attrs: cloneAttrs(attrs), inst: build()}
	s.byKey[key] = entry
	//: hand back the series every later overflow will reuse.
	return entry.inst
}
