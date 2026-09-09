// Package metrics — Resource: who produced the telemetry.
package metrics

import "slices"

// ServiceNameKey is the attribute key OpenTelemetry's resource semantic
// conventions reserve for the logical name of the service. It is dotted, like
// every OTel-conventional key — which is exactly why the Prometheus connector
// cannot carry it (see internal/service/metrics/CLAUDE.md §What the Prometheus
// connector loses).
const ServiceNameKey string = "service.name"

// UnknownService is the value the OpenTelemetry specification mandates when a
// producer supplies no service.name. The spec allows appending the executable
// name to it; this SDK does not, because a binary path is not a service
// identity and would silently become one on a dashboard.
const UnknownService string = "unknown_service"

// ResourceValue identifies the ENTITY that produced the telemetry — the
// service, the process, the host. Its attributes are carried ONCE per payload
// rather than on every point, which is the whole reason the concept exists:
// service.name on ten thousand data points is ten thousand copies of one fact.
//
// SchemaURL is deliberately absent. It is optional in the specification, this
// SDK emits no semantic-convention version, and a field that is always empty is
// a placeholder (CLAUDE.md rule 5). It lands the day the SDK pins a convention
// version, next to Attrs, and nothing else about the shape changes.
type ResourceValue struct {
	// Attrs are the producer's attributes, sorted by Key. A ResourceValue
	// built through a Meter always carries ServiceNameKey.
	Attrs []AttrValue
}

// Normalized returns the ResourceValue a Meter publishes: attributes sorted,
// validated, owned, and carrying ServiceNameKey whether or not the caller
// supplied it.
//
// The service.name default is not an SDK invention — the OpenTelemetry
// specification mandates unknown_service for exactly this case, so the clamp
// substitutes nobody's judgement (ADR 0031 §clamp).
func (r ResourceValue) Normalized() ResourceValue {
	//: sort + validate + own; an unusable attribute set panics here, at
	//: construction, rather than at the first export.
	attrs := SortAttrs(r.Attrs)
	//: a supplied service.name is kept exactly as the caller wrote it.
	if slices.ContainsFunc(attrs, isServiceName) {
		//: already identified.
		return ResourceValue{Attrs: attrs}
	}
	//: insert the mandated default at the position the sort would give it.
	name := String(ServiceNameKey, UnknownService)
	at, _ := slices.BinarySearchFunc(attrs, name, CompareAttrKey)
	//: hand back the canonical Resource.
	return ResourceValue{Attrs: slices.Insert(attrs, at, name)}
}

// isServiceName reports whether a carries the reserved service.name key.
func isServiceName(a AttrValue) bool {
	//: key equality alone — any value the caller supplied is theirs to keep.
	return a.Key == ServiceNameKey
}
