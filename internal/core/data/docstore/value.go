package docstore

import (
	"encoding/json"
	"time"
)

// EntryValue is one stored document as the store holds it: its key and its
// JSON, for a caller that shows documents rather than decoding them.
type EntryValue struct {
	// Key is the document's key.
	Key string
	// JSON is the document, a copy the caller owns.
	JSON json.RawMessage
}

// VersionValue is one version of a document: its number, when the write that
// made it ran, what that write's caller said about it, and the document as the
// version holds it.
type VersionValue struct {
	// At is when the write that made the version ran, in UTC, read from the
	// store's clock and kept to the nanosecond — by the file engine for the
	// years 0 to 9999, which RFC 3339 writes, and by the SQL engine for any
	// instant. It is zero for the first version of a document the store held
	// before it kept versions: when that one was written is not known.
	At time.Time
	// Meta is what the write's caller said about it — who made it, which
	// command — as its [StampValue] gave it; nil when it said nothing. The
	// store keeps it and never reads it.
	Meta map[string]string
	// JSON is the document as the version holds it, compact, a copy the
	// caller owns. The newest version's is the document itself.
	JSON json.RawMessage
	// Number counts a document's versions from 1. No number is given twice
	// while the document exists or is in the trash, pruning included; a
	// document deleted, or purged from the trash, and written again is a new
	// document, and starts again at 1.
	Number uint64
}

// StampValue is what a write says about the version it makes, for the
// Stamped writes. Its zero value is what Put, Insert, Replace and Update say:
// nothing, and a new version.
type StampValue struct {
	// Meta is kept with the version the write makes — who made it, which
	// command — and read back with it. The store copies it and never reads
	// it.
	Meta map[string]string
	// InPlace makes the write change the document without making a version:
	// the current version keeps its number, its instant and its Meta, and
	// holds the new document. A framework stamps so a workflow's transition,
	// which its journal already records. A creation is version 1 all the
	// same.
	InPlace bool
}

// TombstoneValue is a document in the trash, as [Trashable].Tombstones
// lists it: its key and when it was trashed. The document and its versions
// are read through [Versioned].Versions.
type TombstoneValue struct {
	// Key is the trashed document's key.
	Key string
	// At is when the document was trashed, in UTC, read from the store's
	// clock and kept to the nanosecond, as a version's instant is.
	At time.Time
}

// VersionWeightValue is what a store's versions weigh, as
// [VersionWeigher].VersionWeight reports it: what the store keeps beyond
// the documents it holds. A document's current version is the document
// itself, and counts in none of its fields. Both engines count the same
// versions the same way, so the same writes weigh the same on either.
type VersionWeightValue struct {
	// Former is how many former versions the store keeps, every document's
	// — a trashed one's included.
	Former int
	// Trashed is how many documents are in the trash ([Trashable]).
	Trashed int
	// Bytes is what the former versions and the trashed documents weigh:
	// the length of the JSON each holds, compact, as both engines keep it.
	// The instants and the metadata they carry are not counted.
	Bytes int64
}
