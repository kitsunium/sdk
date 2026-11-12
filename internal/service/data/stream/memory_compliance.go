package stream

import corestream "github.com/kitsunium/sdk/internal/core/data/stream"

// The memory engine answers the port and four of its siblings; Compactor is
// left to the engines that keep segments or rows to rewrite (#824).
var (
	_ corestream.Stream    = (*memoryStream)(nil)
	_ corestream.Waker     = (*memoryStream)(nil)
	_ corestream.Seeker    = (*memoryStream)(nil)
	_ corestream.Trimmer   = (*memoryStream)(nil)
	_ corestream.Committer = (*memoryStream)(nil)
)
