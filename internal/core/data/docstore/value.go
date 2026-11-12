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

// RevisionValue is the token [Revisioned].Revision returns with a document
// and ReplaceIf compares: opaque, an engine's own, equal to the one read
// while the stored document is the one read. The file engine renders the
// SHA-256 of the document's JSON as json.Marshal writes it, which holds
// across a fold and a reopen; the SQL engine renders the row's revision,
// which every write moves. Compare two only for equality.
type RevisionValue string

// ChangeOp is what a change record says a write did: [ChangePut],
// [ChangeDelete], [ChangeTrash] or [ChangeRestore].
type ChangeOp string

// The four operations a change record names. A store's change stream
// carries one record per write that stood, its [ChangeValue].Op one of
// these.
const (
	// ChangePut records a document stored: Put, Insert, Replace, Update, their
	// Stamped forms, ReplaceIf, a batch's write and PutDocument of a document
	// the store holds.
	ChangePut ChangeOp = "put"
	// ChangeDelete records a document removed for good: Delete, a batch's
	// deletion, and Purge, which empties the trash of one.
	ChangeDelete ChangeOp = "delete"
	// ChangeTrash records a document moved to the trash: Trash, and
	// PutDocument of a trashed document. It leaves every read.
	ChangeTrash ChangeOp = "trash"
	// ChangeRestore records a document Restore brought back from the trash.
	ChangeRestore ChangeOp = "restore"
)

// ChangeValue is one change record of a store's change feed
// ([ChangeFeeder]): the payload of the stream record a write appends, as
// JSON — decode it with encoding/json. The stream record's key is the
// document's key.
//
// A reader rebuilds a store by applying the records of a partition in
// offset order, and replays one idempotently by (Key, Version): a record
// whose version a store already holds, or holds a later one of, changes
// nothing.
type ChangeValue struct {
	// At is when the write ran, in UTC, read from the store's clock and
	// kept to the nanosecond.
	At time.Time `json:"at"`
	// Document is the document's JSON as the store holds it, compact: the
	// stored document for a put or a restore, the trashed one for a trash.
	// It is there only when the store was configured with its
	// ChangesWithDocument, and never for a delete.
	Document json.RawMessage `json:"doc,omitempty"`
	// Key is the document's key.
	Key string `json:"key"`
	// Op is what the write did.
	Op ChangeOp `json:"op"`
	// Version is the number of the document's current version once the
	// write stood — the one a put or a restore leaves, the trashed
	// document's for a trash —, as [Versioned].Versions numbers them; 1
	// for a document stored before its store kept versions, and 0 for a
	// delete and on a store that keeps no versions.
	Version uint64 `json:"version,omitempty"`
}
