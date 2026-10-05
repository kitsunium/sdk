package otlp

import coreotel "github.com/kitsunium/sdk/internal/core/observe/otel"

// resourceOf is ResourceOf's body: decl_gen.go writes ResourceOf, from the
// design, as one call of it.
func resourceOf(resource coreotel.ResourceValue) ResourceMessage {
	//: one message, its attributes rendered in their canonical order.
	return ResourceMessage{Attributes: Attrs(resource.Attrs)}
}
