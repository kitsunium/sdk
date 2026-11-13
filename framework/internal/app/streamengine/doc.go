// Package streamengine is what framework/app/stream plugs into kit: the log a
// streamed topic appends to — the SDK's stream in memory, in the data
// directory and over SQL —, the consumer groups its subscriptions read it as,
// and the migration of a stream's tables (issue #826, ADR 0179 step 4's
// shape). The framework's core imports none of the SDK's stream: it reaches
// it through plug, and a product that declares no streamed topic links none
// of it.
//
// Package streamengine — the compile-time proof that the streams are the framework's.
package streamengine
