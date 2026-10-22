// Package otlpengine is what framework/app/otlp plugs into the framework:
// the export app.OTLP declares — the product's spans and the metrics the
// framework derives from them, each signal kept in a durable queue of its
// own (the SDK's app/forward over its data/queue, in the data directory, else
// in memory) and forwarded to an OpenTelemetry collector over OTLP/HTTP, or
// to the product's own sender (issue #483, ADR 0185). The framework's core
// imports none of the SDK's forward, queue or OTLP exporters: it reaches them
// through plug, and a product that declares no export links none of them.
//
// Package otlpengine — the compile-time proof that the export is the framework's.
package otlpengine
