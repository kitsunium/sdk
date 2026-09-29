// Package telemetry — the compile-time proof that Exporter is an Emitter.
package telemetry

// : Asserts at compile time that *Exporter satisfies Emitter.
var _ Emitter = (*Exporter)(nil)
