# memory

In-memory sink for the kitsunium logger. Buffers a defensive snapshot of each
received record (level, message, attrs) so tests can assert on what was logged
without parsing an encoder's byte output.

`Write` records, `Records()` returns an independent copy of the buffer, and
`Reset()` clears it. All methods are safe for concurrent use.

Usage is internal; consumers reach it through `pkg/v1/logger.NewMemorySink`.
