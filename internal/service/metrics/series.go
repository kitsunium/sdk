// Package metrics — series identity: the canonical key binding an instrument
// name and an attribute set to exactly one series.
package metrics

import (
	"encoding/binary"
	"slices"
	"strings"

	coremetrics "github.com/kitsunium/sdk/internal/core/metrics"
)

// maxStackAttrs is how many attributes the lookup path sorts without touching
// the heap. Eight is already an unusual number of dimensions on one metric;
// beyond it the sort falls back to a heap clone, which is correct and merely
// slower — the pathological case pays, the normal one does not.
const maxStackAttrs int = 8

// seriesKeyCap is the stack scratch the encoded series key is built in. A key
// is the instrument name plus every key and typed value, each tagged and
// length-prefixed or fixed-width, so 192 bytes covers a long name with a
// handful of attributes. Overflowing it costs one heap slice on that call,
// nothing more.
const seriesKeyCap int = 192

// overflowAttrs is the attribute set of the single aggregated series a name
// folds into once its cardinality bound is reached. Package-level and never
// mutated.
//
// The value is a BOOL, not the string "true". Before typed attributes it had to
// be a string because a value was a string everywhere the snapshot was going;
// that reason expired with the OTel attribute model. The Prometheus rendering
// is byte-identical either way, and an OTLP payload now carries a real boolean.
var overflowAttrs = []coremetrics.AttrValue{coremetrics.Bool(coremetrics.OverflowAttrKey, true)}

// sortAttrs returns attrs ordered by Key, writing into buf when it fits so the
// common case never allocates.
//
// Sorting is what makes an attribute SET a set: without it {a,b} and {b,a}
// encode to two different keys and silently become two series, which both
// doubles cardinality and splits one metric's total across two rows that no
// exporter can recombine.
//
// The result aliases either buf or a fresh heap slice; callers must copy it
// before storing it.
func sortAttrs(buf, attrs []coremetrics.AttrValue) []coremetrics.AttrValue {
	//: the dimensionless series is the common case and needs no work.
	if len(attrs) == 0 {
		//: nil, not an empty slice — it is what gets stored on the entry.
		return nil
	}
	//: sorted borrows the caller's stack scratch whenever the set fits.
	sorted := buf
	//: an oversized set falls back to the heap rather than truncating.
	if len(attrs) > cap(buf) {
		//: fresh backing array; correctness before speed.
		sorted = make([]coremetrics.AttrValue, 0, len(attrs))
	}
	//: copy in, then order by Key.
	sorted = append(sorted, attrs...)
	slices.SortFunc(sorted, coremetrics.CompareAttrKey)
	//: hand back the ordered view.
	return sorted
}

// appendSeriesKey appends the canonical encoding of (kind, name, sorted) to dst
// and returns the grown slice.
//
// Every string is LENGTH-PREFIXED rather than separator-delimited, and every
// value carries its KIND TAG. An attribute value is data — a route, a tenant
// id, a status code — so with a delimiter a caller who can influence one value
// can forge another series' key and have two unrelated series silently
// accumulate into one. Length prefixes make the encoding injective over
// strings; the attribute kind tag extends that injectivity across TYPES, so
// String("v", "1") and Int64("v", 1) stay two series rather than merging into
// one on the strength of spelling the same in decimal.
//
// The INSTRUMENT kind opens the key for a different reason: four instrument
// kinds now share one store, because a Counter and an UpDownCounter produce the
// same point shape. Without it, `Counter("x")` and `UpDownCounter("x")` would
// resolve to the same key, the second call would HIT the read lock, and the
// cross-kind conflict would never reach bindName — one name would silently
// carry both monotonicities. One byte on a stack buffer buys that check back
// without a second map read per observation.
func appendSeriesKey(dst []byte, kind instrumentKind, name string, sorted []coremetrics.AttrValue) []byte {
	//: the instrument kind opens the key, before anything a caller controls.
	dst = append(dst, byte(kind))
	//: then the name.
	dst = appendSized(dst, name)
	//: then every attribute, in the order that made the set canonical.
	for _, attr := range sorted {
		//: the key is a string and is sized like the name.
		dst = appendSized(dst, attr.Key)
		//: the value carries its own tag and its own framing.
		dst = attr.AppendIdentity(dst)
	}
	//: caller owns the grown slice.
	return dst
}

// appendSized appends uvarint(len(s)) followed by s.
func appendSized(dst []byte, s string) []byte {
	//: the length prefix is what makes the encoding unambiguous.
	dst = binary.AppendUvarint(dst, uint64(len(s)))
	//: then the raw bytes — no escaping needed, the length already bounds them.
	return append(dst, s...)
}

// cloneAttrs returns an owned copy of src, or nil for the dimensionless set.
// The copy is what lets the meter share its attribute slice with every snapshot
// while the caller's stack scratch stays on the stack.
func cloneAttrs(src []coremetrics.AttrValue) []coremetrics.AttrValue {
	//: nil in, nil out — the dimensionless series stores no attribute slice.
	if len(src) == 0 {
		//: nothing to own.
		return nil
	}
	//: exact-size backing array; the attribute set never grows after creation.
	return slices.Clone(src)
}

// compareAttrs orders two sorted attribute sets lexicographically — key, then
// the value's canonical identity, then the shorter set first on a common
// prefix. Used at Collect time to make a snapshot's series order deterministic.
func compareAttrs(a, b []coremetrics.AttrValue) int {
	//: compare pairwise over the common prefix.
	for i := range min(len(a), len(b)) {
		//: keys decide first.
		if c := strings.Compare(a[i].Key, b[i].Key); c != 0 {
			//: ordered.
			return c
		}
		//: equal keys fall through to the values. The ordering is total
		//: across kinds and distinguishes every pair the series key keeps
		//: apart, so two distinct series never sort equal and the snapshot
		//: stays byte-deterministic.
		if c := coremetrics.CompareAttrValue(a[i], b[i]); c != 0 {
			//: ordered.
			return c
		}
	}
	//: a common prefix is broken by the shorter set.
	return len(a) - len(b)
}
