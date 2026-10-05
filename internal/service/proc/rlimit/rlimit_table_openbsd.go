//go:build openbsd

package rlimit

import coreproc "github.com/kitsunium/sdk/internal/core/proc"

// addPlatformLimits is a no-op on OpenBSD: the kernel exports no RLIMIT_* beyond
// the common set, so ResourceAS stays unmapped (UnknownResource).
func addPlatformLimits(_ map[coreproc.Resource]int) {
	//: OpenBSD exposes no extra rlimit resource; the common table is complete.
}
