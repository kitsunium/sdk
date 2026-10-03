// Package metrics — the sibling port that documents an instrument NAME.
package metrics

// Describer attaches a human-readable description to an instrument NAME.
//
// The OTel metrics data model puts `description` on the Metric, not on the data
// point, and the specification says outright that it is NON-IDENTIFYING: two
// streams that differ only by their description are one stream. That is why
// this is Describe(name, …) and not a description parameter threaded through
// Counter/Gauge/Histogram. A description describes the thing a backend graphs,
// which is the name; a call site only ever contributes one series of it, and a
// per-call-site parameter would invite two call sites to disagree about the
// documentation of one metric while both look correct.
//
// It is also why the description is not on the observation path at all. Counter
// and its siblings are called once per observation; Describe is called once, at
// wiring time, next to the code that decides the instrument exists.
//
// # A sibling, discovered by type assertion
//
// Meter is FROZEN (ADR 0039) and so is FullMeter — a union is still an
// interface, and adding a method to either would break every downstream double
// at compile time with no deprecation window. Describer is therefore a fourth
// sibling beside UpDownMeter and AsyncMeter, and it is NOT folded into
// FullMeter:
//
//	if d, ok := meter.(metrics.Describer); ok {
//		d.Describe("http_server_requests", "Requests served, by route and status")
//	}
//
// The assertion is not ceremony. A Meter that records no description — a test
// double, a no-op meter, a downstream implementation written before this port
// existed — legitimately does not implement Describer, and `ok == false` is how
// a caller finds out that their documentation will not reach the wire. The same
// shape as cache.Tagger (ADR 0049) and lock.Deadliner (ADR 0052): the ABSENCE
// of the sibling is the answer.
//
// # What an implementation is expected to refuse
//
// Both refusals are programmer errors — a description is a literal written at a
// wiring site, constant for the process — so an implementation is expected to
// refuse them LOUDLY rather than return an error this signature has nowhere to
// put, exactly as Meter refuses a cross-kind name reuse:
//
//   - An EMPTY description. It is a call that does nothing, which is the inert
//     outcome ADR 0031 bans; the caller meant to write something.
//   - A SECOND, DIFFERENT description for one name. A description belongs to
//     the name, so two of them means one of the two wiring sites is wrong, and
//     whichever one lost would be invisible on every wire. Re-describing a name
//     with the SAME text is idempotent, because two packages documenting one
//     metric identically have not disagreed about anything.
//
// A description is DATA, not structure: unlike an instrument name or an
// attribute key, it is prose, and an implementation must not refuse it for the
// bytes it contains. An exporter escapes it for the wire it is writing.
type Describer interface {
	// Describe binds description to the instrument called name. It is
	// idempotent for identical text and refused for conflicting text.
	Describe(name, description string)
}
