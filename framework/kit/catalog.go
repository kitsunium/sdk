package kit

import (
	ikit "github.com/kitsunium/sdk/framework/internal/kit"
	"github.com/kitsunium/sdk/framework/model"
)

// catalog is Catalog's body: decl_gen.go writes Catalog, from the
// design, as one call of it.
func catalog() []model.Mechanic {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.Catalog()
}

// newID is NewID's body: decl_gen.go writes NewID, from the
// design, as one call of it.
func newID(prefix string) string {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.NewID(prefix)
}
