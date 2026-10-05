package kit

import (
	ikit "github.com/kitsunium/sdk/framework/internal/kit"
)

// Frontend is static assets the product serves: a single page application,
// a documentation site. Requests the page makes to the product's endpoints
// are attributed to it in the diagram.
type Frontend = ikit.Frontend

// StaticOption configures a frontend.
type StaticOption = ikit.StaticConfigurer

// root is Root's body: decl_gen.go writes Root, from the
// design, as one call of it.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func root(dir string) StaticOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.Root(dir)
}
