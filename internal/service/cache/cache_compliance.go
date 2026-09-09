// Package cache — hosts the compile-time interface assertions, keeping them
// out of the production source so the runtime binary carries no
// diagnostic-only declarations.
package cache

import corecache "github.com/kitsunium/sdk/internal/core/cache"

// Compile-time assertions that both stores still answer every contract they
// claim. They matter more here than in most packages: NewMemory and NewChain
// return the PORT, and the three capabilities beyond it are reached by type
// assertion — so a sibling silently dropped would not fail to compile anywhere
// and would not fail at run time either. It would take the caller's `ok ==
// false` branch, which is written as the graceful fallback. A cache that
// quietly stops being invalidatable is exactly the defect ADR 0049 spends its
// tag section preventing.
//
// Collecting them also makes the set greppable: every type that must satisfy a
// port is named here, so a port that quietly lost an implementation is one diff
// away from being visible.
var (
	_ corecache.Store[struct{}]        = (*memoryStore[struct{}])(nil)
	_ corecache.EntryFetcher[struct{}] = (*memoryStore[struct{}])(nil)
	_ corecache.Tagger                 = (*memoryStore[struct{}])(nil)
	_ corecache.Loader[struct{}]       = (*memoryStore[struct{}])(nil)
	_ corecache.Store[struct{}]        = (*chainStore[struct{}])(nil)
	_ corecache.EntryFetcher[struct{}] = (*chainStore[struct{}])(nil)
	_ corecache.Tagger                 = (*chainStore[struct{}])(nil)
	_ corecache.Loader[struct{}]       = (*chainStore[struct{}])(nil)
)
