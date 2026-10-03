// Package git — the compile-time proof that ChangedSetValue still satisfies the
// core port.
//
// Hoisted out of changed_set.go per KTN-IFACE-ASSERT-PLACEMENT: the production
// source carries no purely verificational declarations. Without this assertion a
// method renamed here would only fail at the call site that still expected the
// old name, which may be in another repository.
package git

import coregit "github.com/kitsunium/sdk/framework/internal/core/git"

// _ asserts at compile time that the concrete changed set still implements the
// core port, so a renamed method fails here rather than at a consumer's call
// site.
var _ coregit.ChangedSet = (*ChangedSetValue)(nil)
