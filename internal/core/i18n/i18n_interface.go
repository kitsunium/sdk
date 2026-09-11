// Package i18n — the message-source port and its two ADR 0039 siblings.
package i18n

// Catalog is a source of translated messages, FROZEN at two methods
// (ADR 0039). pkg/v1/i18n aliases it, Go interfaces are structural, and adding
// a third method would break every downstream two-method double at compile
// time with no deprecation window.
//
// # Lookup is EXACT and does nothing clever
//
// It answers for the pair it was handed and performs no fallback, no parent
// truncation ("fr-CA" does not become "fr") and no plural selection. Every one
// of those is a policy: which language stands in for which, and whether a
// missing translation is an error or a silent substitution. A Catalog that
// decided them would decide them for every caller, and the decision would be
// invisible at the call site.
//
// The walk therefore lives in exactly one place — internal/service/i18n's
// renderer — which knows which tag actually answered and can pick that
// language's plural rules. See the package comment.
//
// Implementations MUST be safe for concurrent use and MUST NOT mutate after
// construction: one Catalog is shared by every goroutine serving a request.
type Catalog interface {
	// Lookup returns the message registered for exactly (tag, key), and
	// reports whether there was one.
	Lookup(tag TagValue, key Key) (message MessageValue, ok bool)
	// Tags reports every tag this catalog can answer for, sorted by their
	// canonical string so the order is stable across runs.
	Tags() []TagValue
}

// KeyLister is an ADR 0039 sibling: a [Catalog] that can enumerate the keys it
// holds for a tag. It is discovered by type assertion, exactly as codec's
// Appender and cache's Tagger are, so [Catalog] stays frozen at two methods.
//
// It exists for one job, and that job is a test rather than a request: a
// caller compares the key set of its fallback language against the key set of
// every other, and fails its own suite when a translation is missing. That
// moves "this string was never translated" from a report a user writes to a
// build that does not go out — which is the only place the SDK can help,
// because at render time the string is already missing and something has to be
// shown.
type KeyLister interface {
	Catalog
	// Keys reports the keys held for tag, sorted, or nil when the catalog
	// does not know the tag at all.
	Keys(tag TagValue) []Key
}

// Fallbacker is an ADR 0039 sibling: a [Catalog] that names the language it
// falls back to when a key is absent from the one requested.
//
// It is a sibling and not a [Catalog] method because a catalog that has no
// fallback is a legitimate, useful thing — a bundle for one language, or a
// test double — and the absence of the method is how a renderer finds out,
// rather than a zero [TagValue] it would have to interpret. That is ADR 0039's
// shape and ADR 0052's lesson: the in-process lease implements Deadliner and
// the file lease does not, and the absence IS the answer.
type Fallbacker interface {
	Catalog
	// Fallback reports the tag this catalog falls back to. It is never the
	// zero TagValue: a catalog that has no fallback does not implement this
	// interface.
	Fallback() TagValue
}
