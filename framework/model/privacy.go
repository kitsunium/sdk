// Personal data: the fields a store classifies, erasure, retention, legal
// holds and the privacy journal.

package model

import (
	"github.com/kitsunium/sdk/framework/model/internal/core"
)

// The modes of KIT_RETENTION.
const (
	// RetentionOn erases and deletes what is due: the default.
	RetentionOn string = core.RetentionOn
	// RetentionDryRun journals what the retention would do and changes
	// nothing: how a new retention is deployed over old data.
	RetentionDryRun string = core.RetentionDryRun
	// RetentionOff runs no retention, warned of at every start outside dev.
	RetentionOff string = core.RetentionOff
)

// The gaps a register lists for a store that keeps personal data.
const (
	GapPurpose   string = core.GapPurpose   // no kit.Purpose
	GapRetention string = core.GapRetention // no retention: its personal data is kept forever
	GapSubject   string = core.GapSubject   // no subject field: no person can have their records
)

// The security measures a register lists (point g).
const (
	// MeasureRedacted: the Studio, the spans and the logs never show a
	// personal, special or secret value.
	MeasureRedacted string = core.MeasureRedacted
	// MeasureHolds: a legal hold stops erasure and deletion.
	MeasureHolds string = core.MeasureHolds
	// MeasureJournal: every export, erasure, deletion and hold is journaled,
	// each entry chained to the previous one by SHA-256.
	MeasureJournal string = core.MeasureJournal
	// MeasureNotSealed: the store's classified members are kept in clear at
	// rest, until kit's sealing lands (ADR 0006, step 3).
	MeasureNotSealed string = core.MeasureNotSealed
	// MeasureSealed: its classified members are sealed at rest, AES-256-GCM
	// under per-subject data keys (ADR 0006, step 3).
	MeasureSealed string = core.MeasureSealed
)

// The operations of the privacy journal.
const (
	JournalExport      string = core.JournalExport      // a person's records exported
	JournalRead        string = core.JournalRead        // a record read by a module, through kit.Records
	JournalErase       string = core.JournalErase       // a record's personal data cleared
	JournalDelete      string = core.JournalDelete      // a record removed
	JournalEraseFields string = core.JournalEraseFields // some fields of a record cleared, by a module
	JournalHold        string = core.JournalHold        // a hold placed on a record
	JournalRelease     string = core.JournalRelease     // a hold lifted
	JournalShred       string = core.JournalShred       // a subject's data key destroyed (ADR 0006, step 3)
)

type (
	// PersonalData is a person's data as kit.Export gives it (GDPR art. 15 and
	// 20): their records, store by store, with what art. 15(1) asks beside them.
	PersonalData = core.PersonalDataMessage
)

type (
	// StoreData is one store's records about a person.
	// It is one part of what kit.Export gives a person about themselves.
	StoreData = core.StoreDataMessage
)

type (
	// Erasure is what kit.Erase did, store by store.
	// Stores lists, store by store, what the erasure did there.
	Erasure = core.ErasureMessage
)

type (
	// StoreErasure is one store's part of an erasure: the keys of the records
	// erased, deleted, and held — left in place, as a legal hold asks. A key
	// may be personal data, which the Studio and the logs never show.
	StoreErasure = core.StoreErasureMessage
)

type (
	// Privacy is what a running app says of the personal data it keeps.
	// It is the Privacy page's data: the register, the holds and the journal's
	// state.
	Privacy = core.PrivacyMessage
)

type (
	// Register is the record of processing of GDPR art. 30(1), as far as the
	// code knows it.
	Register = core.RegisterMessage
)

type (
	// RegisterStore is one store's line in the register.
	// It names the classes the store keeps, their purposes and their retention.
	RegisterStore = core.RegisterStoreMessage
)

type (
	// Hold is one record a legal hold keeps. It carries references only: never
	// the record's key, its subject's identity, or the hold's reason, which kit
	// keeps apart.
	Hold = core.HoldEvent
)

type (
	// JournalEntry is one operation of the privacy journal: what was done,
	// where, by whom and when, with references only — never an identity, a
	// value or a key. Each entry is chained to the previous one by SHA-256.
	JournalEntry = core.JournalEntryEvent
)

type (
	// JournalCheck is what verifying the journal's chain found.
	// A broken chain names the first entry whose hash does not follow.
	JournalCheck = core.JournalCheckMessage
)

type (
	// RecordFormer is the former values of one exported record's fields.
	// A personal or special field's former values are redacted, never exported in
	// clear.
	RecordFormer = core.RecordFormerMessage
)
