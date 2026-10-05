package profiling

import (

	// registers "gzip": Parse, and so every capture, inflates the runtime's
	// gzipped profiles through the transform scheme it names.
	_ "github.com/kitsunium/sdk/internal/service/data/transform"
)
