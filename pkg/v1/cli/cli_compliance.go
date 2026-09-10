// Package cli — hosts the compile-time interface assertions, keeping them out
// of the production source so the runtime binary carries no diagnostic-only
// declarations.
package cli

import coreconfig "github.com/kitsunium/sdk/internal/core/config"

// Compile-time proof that a [FlagSourceValue] IS a configuration source.
//
// ADR 0065 §D6's whole claim is that `cli` COMPOSES `config` rather than
// reimplementing it, and the composition is exactly one thing: the flags an
// operator typed can be handed to a `config.Load` as its last layer. That
// claim is worth precisely what its mechanical enforcement is worth — a doc
// line saying "satisfies config.Source" survives a signature change on either
// side and goes on saying it; this does not.
//
// It is the negative that would be expensive here. If `Source` grew a method,
// or `Load` changed shape, the failure without this line would not be a build
// error: it would be a consumer discovering at their own call site that the
// SDK's own documented one-liner no longer compiles.
var _ coreconfig.Source = (*FlagSourceValue)(nil)
