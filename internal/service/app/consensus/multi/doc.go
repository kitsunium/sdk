// Package multi packs many small commands into one consensus entry and splits
// their results, on top of the Replicated port and with no change to the
// engine (ADR 0152 D6, D19, #926). A [Packer] packs the commands its callers
// propose at one moment into one length-prefixed body and issues ONE
// Propose; a [Machine] on every node splits that body and applies the
// commands in order through its inner BatchingStateMachine, as one batch,
// and the Packer answers each caller with the entry's token, the command's
// index in it and its own result.
//
// An entry carries a header, a slot, a checksum and an apply result
// whatever its size: for commands of a few dozen bytes, the entry count,
// not the bytes, bounds a leader's turn and a follower's parse.
package multi
