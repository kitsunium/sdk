package studiokit

import "github.com/kitsunium/sdk/framework/internal/kit/plug"

// : Asserts at compile time that eventStream is the stream plug.EventStream
// describes, and translator the catalogues plug.Translator describes.
var (
	_ plug.EventStream = eventStream{}

	_ plug.Translator = (*translator)(nil)
)
