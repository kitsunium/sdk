package serverengine

import "github.com/kitsunium/sdk/framework/internal/app/plug"

// : Asserts at compile time that httpServer is the engine plug.HTTPServer
// describes.
var _ plug.HTTPServer = httpServer{}
