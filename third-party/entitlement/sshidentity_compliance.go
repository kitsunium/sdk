// Package entitlement — compile-time proof that the ssh implementation still
// satisfies the port it is written against.
package entitlement

import (
	coreent "github.com/kitsunium/sdk/internal/core/entitlement"
)

// _ asserts at compile time that the ssh implementation still satisfies the
// port, so a method added to Identity fails here rather than at a consumer.
var _ coreent.Identity = (*SSHIdentity)(nil)
