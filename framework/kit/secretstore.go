package kit

import (
	ikit "github.com/kitsunium/sdk/framework/internal/kit"
	"github.com/kitsunium/sdk/pkg/v1/security/secret"
)

// secretStore is SecretStore's body: decl_gen.go writes SecretStore, from the
// design, as one call of it.
func secretStore(store secret.Store) AppOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.SecretStore(store)
}
