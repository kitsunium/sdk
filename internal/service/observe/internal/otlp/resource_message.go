package otlp

import coreotel "github.com/kitsunium/sdk/internal/core/observe/otel"

// ResourceOf renders the shared ResourceValue a Meter or a Tracer publishes.
// Its attributes are already sorted, validated and owned, so this is a field
// copy and nothing else.
func ResourceOf(resource coreotel.ResourceValue) ResourceMessage {
	//: one message, its attributes rendered in their canonical order.
	return ResourceMessage{Attributes: Attrs(resource.Attrs)}
}
