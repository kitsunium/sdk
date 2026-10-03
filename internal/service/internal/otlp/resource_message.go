// Package otlp — resource.v1.Resource, carried once per payload by every
// signal.
package otlp

import coreotel "github.com/kitsunium/sdk/internal/core/otel"

// ResourceMessage is resource.v1.Resource: attributes (1).
//
// droppedAttributesCount (2) is absent because this SDK drops no resource
// attribute. What each signal does drop is a different thing, counted where it
// happens: the meter folds excess SERIES into an overflow series, and a
// Recorder drops whole SPANS when it is full.
type ResourceMessage struct {
	Attributes []KeyValue `json:"attributes,omitempty"`
}

// ResourceOf renders the shared ResourceValue a Meter or a Tracer publishes.
// Its attributes are already sorted, validated and owned, so this is a field
// copy and nothing else.
func ResourceOf(resource coreotel.ResourceValue) ResourceMessage {
	//: one message, its attributes rendered in their canonical order.
	return ResourceMessage{Attributes: Attrs(resource.Attrs)}
}
