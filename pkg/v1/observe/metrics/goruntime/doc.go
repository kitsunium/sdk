// Package goruntime publishes the Go runtime's instruments on a metrics meter
// (#868): RuntimeInstruments registers them with the OpenTelemetry Go runtime
// conventions' names, kinds and units. It stands apart from metrics because
// it reads runtime/metrics, which a program pays for by importing it: only a
// program that registers the runtime's instruments imports this package.
package goruntime
