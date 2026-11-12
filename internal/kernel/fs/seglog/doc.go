// Package seglog is a durable, append-only log of records addressed by a
// contiguous uint64 index, kept in a directory of fixed-size, zero-filled
// segment files: the segmented log the consensus file log wrote for itself
// (ADR 0152 D4), extracted without its vocabulary so that any engine needing
// an ordered, crash-recovering sequence of byte payloads builds on it rather
// than rewriting it (ADR 0159, ADR 0230).
//
// # The record
//
// Every record is 8-byte aligned and little-endian:
//
//	length u32 | crc32c u32 | version u8 | kind u8 | end u8 | tag u8 |
//	batch u64 | index u64 | payload | zero padding
//
// length counts the bytes after the crc, padding excluded, and the crc is the
// CRC-32C of those bytes. The tag is the caller's kind byte, stored and given
// back, never read. Every segment opens with a checkpoint record — the index its
// first entry takes, the log's first index and its batch sequence number — and
// a full segment is closed by a seal record before the next one is published.
//
// # One batch, two syncs
//
// [Log.Append] only buffers. [Log.Sync] writes the pending batch, its last
// record flagged end-of-batch, in two steps: every record but the last, made
// durable, then the last one, made durable. A valid end record therefore proves
// its whole batch was durable before it, and batch k+1 is written only once
// batch k is, so a valid record of a later batch proves every earlier one. A
// failed write or sync is final: the log refuses every later call with Failed,
// because what a failed fsync left on the disk is not knowable.
//
// # Recovery classifies before it repairs
//
// [Open] reads every segment and classifies every hole BEFORE it changes a byte.
// A hole in the last segment whose batch never completed — no valid end record,
// no valid record of a later batch after it — is a torn tail: never synced, so
// never acknowledged; it is zeroed and synced, and the log continues before it.
// Any other hole is damage to synced data and Open refuses with Damaged, the
// files left as they were; a record a newer layout wrote is refused with
// VersionUnsupported, never skipped. What a damaged log means — quarantine,
// repair, a rebuild from elsewhere — is the caller's decision.
//
// # Dropping a prefix
//
// [Log.DropBefore] records the log's new first index durably, then unlinks the
// whole sealed segments that hold only entries below it and syncs the
// directory. Nothing is rewritten: retention is by whole segments, and an entry
// below the first index that still sits in a retained segment is unreadable.
//
// # What it is not
//
// It holds one directory exclusively and takes no lock on it: a caller that may
// be started twice over the same directory locks it first (flock). It knows
// nothing of terms, votes, offsets in a partition or consumers; those are its
// callers' (ADR 0230). One writer at a time, any number of readers: an appended
// entry is readable before it is synced.
package seglog
