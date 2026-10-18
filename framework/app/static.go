package app

import (
	iapp "github.com/kitsunium/sdk/framework/internal/app"
)

// Frontend is static assets the product serves: a single page application,
// a documentation site. Requests the page makes to the product's endpoints
// are attributed to it in the diagram.
type Frontend = iapp.Frontend

// StaticOption configures a frontend.
type StaticOption = iapp.StaticConfigurer

// Root serves the sub-directory dir of the file system rather than its root —
// "assets" for a //go:embed assets directive.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func Root(dir string) StaticOption {
	//: the implementation is framework/internal/app's; this facade only forwards.
	return iapp.Root(dir)
}
