package consensus

// unroutedFrames is the count of unroutedGroup. Only the tests read it, so
// it lives with them rather than as production code nothing calls.
func (t *TCPTransport) unroutedFrames() int64 { return t.unrouted.Load() }
