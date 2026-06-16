//go:build openbsd

// Package rlimit — OpenBSD adds no resources beyond the common Unix set:
// RLIMIT_AS is absent from its kernel ABI, so ResourceAS stays unmapped and a
// request for it surfaces the typed UnknownResource — the honest "this platform
// cannot set that limit" answer — rather than a build failure on the missing
// constant.
package rlimit

import coreproc "github.com/kitsunium/sdk/internal/core/proc"

// addPlatformLimits is a no-op on OpenBSD: the kernel exports no RLIMIT_* beyond
// the common set, so ResourceAS stays unmapped (UnknownResource).
func addPlatformLimits(_ map[coreproc.Resource]int) {
	//: OpenBSD exposes no extra rlimit resource; the common table is complete.
}
