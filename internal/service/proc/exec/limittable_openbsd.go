//go:build openbsd

// Package exec — OpenBSD has no address-space rlimit (RLIMIT_AS is absent from
// its kernel ABI), so it adds no resources beyond the common Unix set. ResourceAS
// therefore stays unmapped on OpenBSD and a Spec requesting it surfaces the typed
// UnknownResource — the honest "this platform cannot set that limit" answer,
// rather than a build failure on the missing constant.
package exec

import coreproc "github.com/kitsunium/sdk/internal/core/proc"

// addPlatformLimits is a no-op on OpenBSD: the kernel exports no RLIMIT_* beyond
// the common set, so ResourceAS stays unmapped (UnknownResource).
func addPlatformLimits(_ map[coreproc.Resource]int) {
	//: OpenBSD exposes no extra rlimit resource; the common table is complete.
}
