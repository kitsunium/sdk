package consensus

// A snapshot handle is handed to the engine as a snapshot, but it also
// satisfies segFile through the *os.File it embeds. The assertion pins that
// down: a change to the handle's method set that dropped one of segFile's
// methods would otherwise surface only where a test wraps it.
var _ segFile = (*fileHandle)(nil)
