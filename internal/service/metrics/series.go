// Package metrics — series identity: the canonical key binding an instrument
// name and a label set to exactly one series.
package metrics

import (
	"encoding/binary"
	"slices"
	"strings"

	coremetrics "github.com/kitsunium/sdk/internal/core/metrics"
)

// maxStackLabels is how many labels the lookup path sorts without touching the
// heap. Eight is already an unusual number of dimensions on one metric; beyond
// it the sort falls back to a heap clone, which is correct and merely slower —
// the pathological case pays, the normal one does not.
const maxStackLabels int = 8

// seriesKeyCap is the stack scratch the encoded series key is built in. A key
// is the instrument name plus every key and value, each length-prefixed, so
// 192 bytes covers a long name with a handful of labels. Overflowing it costs
// one heap slice on that call, nothing more.
const seriesKeyCap int = 192

// overflowLabels is the label set of the single aggregated series a name folds
// into once its cardinality bound is reached. Package-level and never mutated.
var overflowLabels = []coremetrics.LabelValue{{
	Key:   coremetrics.OverflowLabelKey,
	Value: coremetrics.OverflowLabelValue,
}}

// sortLabels returns labels ordered by Key, writing into buf when it fits so
// the common case never allocates.
//
// Sorting is what makes a label SET a set: without it {a,b} and {b,a} encode to
// two different keys and silently become two series, which both doubles
// cardinality and splits one metric's total across two rows that no exporter
// can recombine.
//
// The result aliases either buf or a fresh heap slice; callers must copy it
// before storing it.
func sortLabels(buf, labels []coremetrics.LabelValue) []coremetrics.LabelValue {
	//: the dimensionless series is the common case and needs no work.
	if len(labels) == 0 {
		//: nil, not an empty slice — it is what gets stored on the entry.
		return nil
	}
	//: sorted borrows the caller's stack scratch whenever the set fits.
	sorted := buf
	//: an oversized set falls back to the heap rather than truncating.
	if len(labels) > cap(buf) {
		//: fresh backing array; correctness before speed.
		sorted = make([]coremetrics.LabelValue, 0, len(labels))
	}
	//: copy in, then order by Key.
	sorted = append(sorted, labels...)
	slices.SortFunc(sorted, compareByKey)
	//: hand back the ordered view.
	return sorted
}

// compareByKey orders two labels by Key. A package-level function value, not a
// closure, so passing it to SortFunc allocates nothing.
func compareByKey(a, b coremetrics.LabelValue) int {
	//: Key alone decides the order; duplicates are rejected, not tie-broken.
	return strings.Compare(a.Key, b.Key)
}

// validateLabels panics when sorted cannot name a series — an empty Key, or the
// same Key twice. It runs on the ALREADY SORTED set so duplicates are adjacent
// and the check is one comparison per label.
//
// Panicking is the same call the meter already makes for a cross-kind name
// reuse, and it is safe for the same reason: a label key is structure written
// at the call site, never data. It is wrong on the first call or never, so the
// panic fires in development, deterministically, at the line that caused it.
// The alternative is a series that no exporter can emit (Prometheus and OTLP
// both reject an empty label name) failing far away, inside the component the
// SDK told the caller to stop thinking about.
func validateLabels(sorted []coremetrics.LabelValue) {
	//: walk once; the set is sorted, so a duplicate sits next to its twin.
	for i, label := range sorted {
		//: an empty key names no dimension.
		if label.Key == "" {
			//: fail at the call site that wrote it.
			panic(coremetrics.InvalidLabel.Error())
		}
		//: the same key twice means the set is not a set.
		if i > 0 && sorted[i-1].Key == label.Key {
			//: same refusal — the label set is unusable either way.
			panic(coremetrics.InvalidLabel.Error())
		}
	}
}

// appendSeriesKey appends the canonical encoding of (name, sorted) to dst and
// returns the grown slice.
//
// Every string is LENGTH-PREFIXED rather than separator-delimited. A label
// value is data — a route, a tenant id, a status line — so with a delimiter a
// caller who can influence one value can forge another series' key and have
// two unrelated series silently accumulate into one. Length prefixes make the
// encoding injective: there is exactly one (name, labels) that produces a
// given key.
func appendSeriesKey(dst []byte, name string, sorted []coremetrics.LabelValue) []byte {
	//: the name opens the key.
	dst = appendSized(dst, name)
	//: then every label, in the order that made the set canonical.
	for _, label := range sorted {
		//: key then value, both sized.
		dst = appendSized(dst, label.Key)
		dst = appendSized(dst, label.Value)
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

// cloneLabels returns an owned copy of src, or nil for the dimensionless set.
// The copy is what lets the meter share its label slice with every snapshot
// while the caller's stack scratch stays on the stack.
func cloneLabels(src []coremetrics.LabelValue) []coremetrics.LabelValue {
	//: nil in, nil out — the dimensionless series stores no label slice.
	if len(src) == 0 {
		//: nothing to own.
		return nil
	}
	//: exact-size backing array; the label set never grows after creation.
	return slices.Clone(src)
}

// compareLabels orders two sorted label sets lexicographically — key, then
// value, then the shorter set first on a common prefix. Used at Collect time
// to make a snapshot's series order deterministic.
func compareLabels(a, b []coremetrics.LabelValue) int {
	//: compare pairwise over the common prefix.
	for i := range min(len(a), len(b)) {
		//: keys decide first.
		if c := strings.Compare(a[i].Key, b[i].Key); c != 0 {
			//: ordered.
			return c
		}
		//: equal keys fall through to the values.
		if c := strings.Compare(a[i].Value, b[i].Value); c != 0 {
			//: ordered.
			return c
		}
	}
	//: a common prefix is broken by the shorter set.
	return len(a) - len(b)
}
