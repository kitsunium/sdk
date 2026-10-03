// Package events — hosts DispatchValue, the report of one Publish.
package events

// DispatchValue reports what one [Bus.Publish] did: how many listeners ran,
// whether one of them stopped the dispatch and which, how many never ran
// because of that, and how many failed.
//
// Every field is a scalar, deliberately. A per-listener slice would allocate
// on every publish — including the overwhelmingly common publish in which
// nothing goes wrong — and the per-listener detail already exists where it
// belongs: each collected error carries the listener's name and the event's
// type as fields. The report answers "what happened to this dispatch"; the
// errors answer "to whom".
type DispatchValue struct {
	// Delivered counts the listeners that ran and returned. A listener that
	// returned an error is delivered — it saw the event — and is also
	// counted in Failed. A listener that panicked is NOT delivered.
	Delivered int
	// Failed counts the listeners that returned a non-nil error other than
	// [Halt], plus the listeners that panicked, plus a [Halt] returned
	// without [SubscriptionValue.MayHalt].
	Failed int
	// Halted reports that a listener returned [Halt] and the dispatch stopped
	// there. It is not a failure: [Bus.Publish] returns a nil error for a
	// dispatch whose only remarkable event was a halt.
	Halted bool
	// HaltedBy names the listener that halted, and is empty when Halted is
	// false. It is the whole answer to "why did the last three listeners not
	// run", which is otherwise a question only a debugger can settle.
	HaltedBy string
	// Skipped counts the listeners that were registered for this event type
	// and never ran because the dispatch was halted before reaching them.
	Skipped int
}
