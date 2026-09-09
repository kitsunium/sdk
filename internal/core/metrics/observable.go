// Package metrics — the asynchronous (observable) instruments: a value read at
// collection time rather than written at observation time.
package metrics

// ObserveInt64 reports ONE integer measurement, with its own attribute set,
// from inside an Int64Callback. It is valid only for the duration of that call;
// retaining it and calling it later observes into a collection that has already
// finished.
//
// It is a FUNC port, not a single-method interface, for the reason ADR 0041
// gives: a published func type cannot grow a method, so it cannot break a
// downstream implementer the way widening an interface would (ADR 0039).
type ObserveInt64 func(value int64, attrs ...AttrValue)

// ObserveFloat64 is ObserveInt64 for a double-valued observable.
type ObserveFloat64 func(value float64, attrs ...AttrValue)

// Int64Callback is read ONCE PER COLLECTION and reports the instrument's
// current ABSOLUTE value — not a delta. That is the OTel contract for an
// asynchronous sum: the callback states what the total is now
// (runtime.NumGoroutine(), bytes allocated since boot), and the SDK differences
// successive observations itself when the meter is a delta reader.
//
// A callback MUST NOT call Collect on the meter that is collecting it, and MUST
// NOT block: it runs inline in the collection, so a slow callback is a slow
// scrape for every other instrument too.
type Int64Callback func(observe ObserveInt64)

// Float64Callback is Int64Callback for a double-valued observable — an
// observable gauge, whose reported value is a sampled reading and therefore has
// no temporality at all.
type Float64Callback func(observe ObserveFloat64)
