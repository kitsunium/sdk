// Package wire holds the consensus domain's hand-written fixed layouts: the
// frame of a Message, the 41-byte Token, SnapshotMeta, Membership and the
// timing contract, and the CRC-32C checksums of an entry's body and chain
// (ADR 0152 D4, D18). They are not codec formats: two nodes configured with
// different format strings would write different bytes for one entry, which
// is a fork.
package wire
