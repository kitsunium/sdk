package stream

import corestream "github.com/kitsunium/sdk/internal/core/data/stream"

// The SQL engine answers the port and four of its siblings; Compactor is
// left to #824.
var (
	_ corestream.Stream    = (*sqlStream)(nil)
	_ corestream.Waker     = (*sqlStream)(nil)
	_ corestream.Seeker    = (*sqlStream)(nil)
	_ corestream.Trimmer   = (*sqlStream)(nil)
	_ corestream.Committer = (*sqlStream)(nil)
)
