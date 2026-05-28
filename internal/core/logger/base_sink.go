// Package logger — BaseSink embeddable identity holder. Concrete sinks
// embed *BaseSink to inherit the upcoming Name/Class/Schemes accessors
// without per-impl boilerplate; the struct is constructed once and never
// mutated.
package logger

import "slices"

// minDuplicateLen is the smallest slice length that can contain a duplicate.
// A slice with 0 or 1 element trivially cannot duplicate.
const minDuplicateLen int = 2

// BaseSink carries the immutable identity triple (name, class, schemes) that
// every Sink reports. Concrete sinks embed *BaseSink so they automatically
// satisfy the upcoming sink identity methods (Name / Class / Schemes — added
// to the Sink interface in PR-04) without per-impl boilerplate.
//
// Pointer-embed only (1 word vs 5-word value embed). The struct is
// constructed once at sink construction and never copied.
type BaseSink struct {
	name    string
	class   SinkClass
	schemes []string
}

// NewBaseSink builds a frozen identity triple. The schemes slice is cloned
// so a caller-side mutation never bleeds into Schemes(). The constructor
// panics on three documented programmer errors:
//
//   - empty name (the registry's primary key cannot be empty)
//   - SinkClass strictly greater than maxKnownSinkClass
//     (a stale binary using a class value the current build does not know)
//   - duplicate scheme strings (would create an ambiguous registry index)
//
// These are construction-time panics, never returned errors — a sink that
// misdeclares its identity is a programmer mistake the test suite must
// catch, not a runtime condition the caller should handle.
func NewBaseSink(name string, class SinkClass, schemes []string) *BaseSink {
	//: empty name fails closed — the registry's primary key cannot be empty.
	if name == "" {
		panic("logger.NewBaseSink: empty name")
	}
	//: out-of-range class fails closed — UnknownClass is allowed (it is the
	//: zero value documenting "missing metadata"), but any value above the
	//: top-defined class is an error.
	if class > maxKnownSinkClass {
		panic("logger.NewBaseSink: SinkClass out of range")
	}
	//: duplicate schemes would split the registry's scheme→Name index so we
	//: refuse construction with a deterministic panic message.
	if hasDuplicate(schemes) {
		panic("logger.NewBaseSink: duplicate scheme string")
	}
	//: defensive clone so a caller-side mutation never bleeds into Schemes().
	return &BaseSink{name: name, class: class, schemes: slices.Clone(schemes)}
}

// Name returns the identity string the sink registers under.
func (b *BaseSink) Name() string {
	//: package-level identifier — frozen at construction.
	return b.name
}

// Class returns the operational profile the sink declared.
func (b *BaseSink) Class() SinkClass {
	//: immutable triple member — no clone needed for a uint8.
	return b.class
}

// Schemes returns the URL schemes this sink handles. The returned slice is
// a defensive clone — caller-side mutation does not affect the sink's
// identity.
func (b *BaseSink) Schemes() []string {
	//: clone every call so the registry slice stays pristine. The cost is a
	//: small allocation per call; Schemes is construction-time and accepts it.
	return slices.Clone(b.schemes)
}

// hasDuplicate reports whether s contains the same string twice. Linear scan
// with a map probe — schemes lists are tiny (usually 1-3 entries), so the
// allocation cost of the map is paid once at construction.
func hasDuplicate(s []string) bool {
	//: empty / single-element slices cannot contain duplicates.
	if len(s) < minDuplicateLen {
		//: trivial false: no second slot to compare against.
		return false
	}
	//: build a set keyed by scheme to detect a second occurrence.
	seen := make(map[string]struct{}, len(s))
	//: iterate once; the first repeated key terminates the scan.
	for _, v := range s {
		//: probe + insert in a single pass.
		if _, ok := seen[v]; ok {
			//: second occurrence: report duplicate immediately.
			return true
		}
		seen[v] = struct{}{}
	}
	//: no duplicates observed across the linear scan.
	return false
}
