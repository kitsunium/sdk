package stream

import (
	"io"

	corestream "github.com/kitsunium/sdk/internal/core/data/stream"
)

// The file engine answers the port, four of its siblings and io.Closer;
// Compactor is #824's.
var (
	_ corestream.Stream    = (*fileStream)(nil)
	_ corestream.Waker     = (*fileStream)(nil)
	_ corestream.Seeker    = (*fileStream)(nil)
	_ corestream.Trimmer   = (*fileStream)(nil)
	_ corestream.Committer = (*fileStream)(nil)
	_ io.Closer            = (*fileStream)(nil)
)
