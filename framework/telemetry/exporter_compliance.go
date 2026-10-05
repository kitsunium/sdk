package telemetry

// : Asserts at compile time that *Exporter satisfies Emitter.
var _ Emitter = (*Exporter)(nil)
