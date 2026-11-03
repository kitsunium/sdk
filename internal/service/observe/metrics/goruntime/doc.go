// Package goruntime publishes the Go runtime's own instruments on a metrics
// meter (#868), under the OpenTelemetry Go runtime semantic conventions. It
// is a package of its own because it reads runtime/metrics, whose
// initialisation every program that imports it pays (ADR 0172, ADR 0179 §7):
// the metrics domain stays free of it, so a program that imports metrics
// alone — framework/app does — pays nothing for instruments it never
// registers.
package goruntime
