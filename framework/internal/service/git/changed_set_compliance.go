package git

import coregit "github.com/kitsunium/sdk/framework/internal/core/git"

// _ asserts at compile time that the concrete changed set still implements the
// core port, so a renamed method fails here rather than at a consumer's call
// site.
var _ coregit.ChangedSet = (*ChangedSetValue)(nil)
