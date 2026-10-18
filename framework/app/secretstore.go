package app

import (
	iapp "github.com/kitsunium/sdk/framework/internal/app"
	"github.com/kitsunium/sdk/pkg/v1/security/secret"
)

// SecretStore keeps the secrets in store instead of the environment's: a
// test gives a store holding what its provided secrets hold. The
// environment's variables are still read first.
func SecretStore(store secret.Store) AppOption {
	//: the implementation is framework/internal/app's; this facade only forwards.
	return iapp.SecretStore(store)
}
