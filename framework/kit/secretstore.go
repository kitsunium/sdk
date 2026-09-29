// Package kit — where the secrets are kept, per environment.
package kit

import (
	ikit "github.com/kitsunium/sdk/framework/internal/kit"
	"github.com/kitsunium/sdk/pkg/v1/secret"
)

// SecretStore keeps the secrets in store instead of the environment's: a
// test gives a store holding what its provided secrets hold. The
// environment's variables are still read first.
func SecretStore(store secret.Store) AppOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.SecretStore(store)
}
