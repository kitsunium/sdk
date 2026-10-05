package studiokit

import "github.com/kitsunium/sdk/framework/internal/kit/plug"

// : Asserts at compile time that eventStream is the stream plug.EventStream
// describes.
var _ plug.EventStream = eventStream{}
