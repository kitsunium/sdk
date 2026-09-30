// Package serverkit — the compile-time proof that the engine is kit's.
package serverkit

import "github.com/kitsunium/sdk/framework/internal/kit/plug"

// : Asserts at compile time that httpServer is the engine plug.HTTPServer
// describes.
var _ plug.HTTPServer = httpServer{}
