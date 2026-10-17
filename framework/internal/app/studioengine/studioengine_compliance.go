package studioengine

import "github.com/kitsunium/sdk/framework/internal/app/plug"

// : Asserts at compile time that translator is the catalogues
// plug.Translator describes.
var _ plug.Translator = (*translator)(nil)
