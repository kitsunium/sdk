// Personal data: the fields a store classifies, erasure, retention, legal
// holds and the privacy journal.

package core

import (
	"encoding/json"
	"time"
)

// The modes of KIT_RETENTION.
const (
	// RetentionOn erases and deletes what is due: the default.
	RetentionOn = "on"
	// RetentionDryRun journals what the retention would do and changes
	// nothing: how a new retention is deployed over old data.
	RetentionDryRun = "dry-run"
	// RetentionOff runs no retention, warned of at every start outside dev.
	RetentionOff = "off"
)

// The gaps a register lists for a store that keeps personal data.
const (
	GapPurpose   = "purpose"   // no kit.Purpose
	GapRetention = "retention" // no retention, kit's or the product's: its personal data is kept forever
	GapSubject   = "subject"   // no subject field: no person can have their records
)

// The security measures a register lists (point g).
const (
	// MeasureRedacted: the Studio, the spans and the logs never show a
	// personal, special or secret value.
	MeasureRedacted = "redacted"
	// MeasureHolds: a legal hold stops erasure and deletion.
	MeasureHolds = "legal-holds"
	// MeasureJournal: every export, erasure, deletion and hold is journaled,
	// each entry chained to the previous one by SHA-256.
	MeasureJournal = "journal"
	// MeasureNotSealed: the store's classified members are in clear: it is
	// kept in memory, where nothing is at rest, or its members are plain.
	MeasureNotSealed = "not-sealed"
	// MeasureSealed: its classified members are sealed at rest, AES-256-GCM
	// under per-subject data keys a person's erasure destroys (ADR 0006 §4).
	MeasureSealed = "sealed"
)

// SealedPlaceholder is what the Studio receives in place of a value kit
// keeps sealed at rest — a record's member in the data browser, a former
// value —: never the value, nor its box (ADR 0006 §9). A value it opens and
// may not show is "[redacted]".
const SealedPlaceholder = "[sealed]"

// The operations of the privacy journal.
const (
	JournalExport      = "export"       // a person's records exported
	JournalRead        = "read"         // a record read by a module, through kit.Records
	JournalErase       = "erase"        // a record's personal data cleared
	JournalDelete      = "delete"       // a record removed
	JournalEraseFields = "erase-fields" // some fields of a record cleared, by a module
	JournalHold        = "hold"         // a hold placed on a record
	JournalRelease     = "release"      // a hold lifted
	JournalShred       = "shred"        // a subject's data key destroyed (ADR 0006, step 3)
)

// Personal data (ADR 0006): what kit.Export gives a person, what kit.Erase
// did, and what a running app says of the personal data it keeps — the
// register of processing, the legal holds and the privacy journal. The
// Studio's Privacy page reads Privacy from GET /_kit/api/privacy, in dev;
// `<product> privacy …` prints the same.

// PersonalDataMessage is a person's data as kit.Export gives it (GDPR art. 15 and
// 20): their records, store by store, with what art. 15(1) asks beside them.
type PersonalDataMessage struct {
	// Stores are the stores that keep records about the person.
	Stores []StoreDataMessage `json:"stores"`
}

// StoreDataMessage is one store's records about a person.
// It is one part of what kit.Export gives a person about themselves.
type StoreDataMessage struct {
	// Store is the store's node ID.
	Store string `json:"store"`
	// Purpose is why the store keeps them.
	Purpose string `json:"purpose,omitempty"`
	// Retention says, in words, how long they keep their personal data.
	Retention string `json:"retention,omitempty"`
	// Recipients are who inside the product reads them, as far as the
	// running app knows.
	Recipients []string `json:"recipients,omitempty"`
	// Records are the records, their secret members left out. They hold the
	// person's data, which the Studio and the logs never show.
	Records []json.RawMessage `json:"records" kit:"personal"`
	// Former are the former values of the records' fields that keep them
	// (ADR 0007), record by record; a secret field's are left out.
	Former []RecordFormerMessage `json:"former,omitempty" kit:"personal"`
	// Versions are the versions of the records a store with revisions keeps
	// (ADR 0007 §3), record by record, their secret members left out.
	Versions []ExportedVersionsMessage `json:"versions,omitempty" kit:"personal"`
}

// ExportedVersionsMessage are the versions of one exported record, as a
// StoreDataMessage carries them beside its records.
type ExportedVersionsMessage struct {
	// Record is the record's position in its StoreData's Records.
	Record int `json:"record"`
	// Versions are its versions, newest first — the record as it is now
	// first —, each without its secret members.
	Versions []RecordVersionMessage `json:"versions"`
}

// ErasureMessage is what kit.Erase did, store by store.
// Stores lists, store by store, what the erasure did there.
type ErasureMessage struct {
	// Stores are the stores that kept records about the person.
	Stores []StoreErasureMessage `json:"stores"`
}

// StoreErasureMessage is one store's part of an erasure: the keys of the records
// erased, deleted, and held — left in place, as a legal hold asks. A key
// may be personal data, which the Studio and the logs never show.
type StoreErasureMessage struct {
	// Store is the store's node ID.
	Store string `json:"store"`
	// Erased are the records whose personal data was cleared.
	Erased []string `json:"erased,omitempty" kit:"personal"`
	// Deleted are the records removed.
	Deleted []string `json:"deleted,omitempty" kit:"personal"`
	// Held are the records a hold keeps, untouched.
	Held []string `json:"held,omitempty" kit:"personal"`
}

// PrivacyMessage is what a running app says of the personal data it keeps.
// It is the Privacy page's data: the register, the holds and the journal's
// state.
type PrivacyMessage struct {
	// Retention is KIT_RETENTION: one of the Retention constants.
	Retention string `json:"retention"`
	// Register is the record of processing, as far as the code knows it.
	Register RegisterMessage `json:"register"`
	// Holds are the records a legal hold keeps, newest first.
	Holds []HoldEvent `json:"holds,omitempty"`
	// Journal is the privacy journal's latest entries, newest first.
	Journal []JournalEntryEvent `json:"journal,omitempty"`
	// Chain says whether the journal's chain holds.
	Chain JournalCheckMessage `json:"chain"`
}

// RegisterMessage is the record of processing of GDPR art. 30(1), as far as the
// code knows it.
type RegisterMessage struct {
	// App is the product.
	App string `json:"app"`
	// ToComplete are the points the code cannot know, for a person to
	// write: "a" (the controller and its representatives), "e" (the
	// transfers).
	ToComplete []string `json:"toComplete,omitempty"`
	// Stores are the stores that keep personal data, by node ID.
	Stores []RegisterStoreMessage `json:"stores,omitempty"`
}

// RegisterStoreMessage is one store's line in the register.
// It names the classes the store keeps, their purposes and their retention.
type RegisterStoreMessage struct {
	// Store is the store's node ID.
	Store string `json:"store"`
	// Purpose is point (b): why the store keeps its records.
	Purpose string `json:"purpose,omitempty"`
	// Subject is point (c)'s category of data subjects: the pointer of the
	// field that says whom each record is about.
	Subject string `json:"subject,omitempty"`
	// SubjectDoc is that field's comment, when the analyzer read it.
	SubjectDoc string `json:"subjectDoc,omitempty"`
	// Personal, Special and Secret are point (c)'s categories of personal
	// data: the fields of each class, as JSON pointers. Special fields are
	// art. 9 data; secret ones are credentials.
	Personal []string `json:"personal,omitempty"`
	Special  []string `json:"special,omitempty"`
	Secret   []string `json:"secret,omitempty"`
	// Recipients are point (d) inside the product: the nodes the graph
	// shows reading the store, and the connectors that carry its data out.
	Recipients []string `json:"recipients,omitempty"`
	// Erase, Delete and HeldUntil are point (f), the time limits, in words.
	Erase     string `json:"erase,omitempty"`
	Delete    string `json:"delete,omitempty"`
	HeldUntil string `json:"heldUntil,omitempty"`
	// ByProduct is point (f) when the product keeps the store's retention
	// itself (kit.RetentionByProduct), in its own words: no retention gap,
	// and no subject gap either.
	ByProduct *ProductRetentionSpec `json:"byProduct,omitempty"`
	// Security is point (g): the measures kit takes.
	Security []string `json:"security,omitempty"`
	// Gaps are the Gap constants the store lacks.
	Gaps []string `json:"gaps,omitempty"`
}

// HoldEvent is one record a legal hold keeps. It carries references only: never
// the record's key, its subject's identity, or the hold's reason, which kit
// keeps apart.
type HoldEvent struct {
	// Store is the store's node ID.
	Store string `json:"store"`
	// Record is the record's reference.
	Record string `json:"record"`
	// Subject is the reference of the person the record is about, when it
	// has one.
	Subject string `json:"subject,omitempty"`
	// By is who placed it: a user's ID, "cli" or "studio".
	By string `json:"by"`
	// At is when.
	At time.Time `json:"at"`
}

// JournalEntryEvent is one operation of the privacy journal: what was done,
// where, by whom and when, with references only — never an identity, a
// value or a key. Each entry is chained to the previous one by SHA-256.
type JournalEntryEvent struct {
	// Seq numbers the entries from 1, with no gap.
	Seq int64 `json:"seq"`
	// At is when, on the app's clock.
	At time.Time `json:"at"`
	// Op is one of the Journal constants.
	Op string `json:"op"`
	// DryRun marks what the retention would have done under
	// KIT_RETENTION=dry-run: nothing changed.
	DryRun bool `json:"dryRun,omitempty"`
	// Store is the store's node ID.
	Store string `json:"store,omitempty"`
	// Record is the record's reference; Subject its subject's.
	Record  string `json:"record,omitempty"`
	Subject string `json:"subject,omitempty"`
	// By is who: a user's ID, the node that called, "retention", "cli" or
	// "studio".
	By string `json:"by"`
	// Hash is the entry's link in the chain: SHA-256 of the previous
	// entry's hash and this entry.
	Hash string `json:"hash"`
}

// JournalCheckMessage is what verifying the journal's chain found.
// A broken chain names the first entry whose hash does not follow.
type JournalCheckMessage struct {
	// Entries is how many entries the journal holds.
	Entries int `json:"entries"`
	// BrokenAt is the sequence number where the chain breaks: an entry
	// changed, or one removed before it. Zero when the chain holds.
	BrokenAt int64 `json:"brokenAt,omitempty"`
}

// RecordFormerMessage is the former values of one exported record's fields.
// A personal or special field's former values are redacted, never exported in
// clear.
type RecordFormerMessage struct {
	// Record is the record's position in its StoreData's Records.
	Record int `json:"record"`
	// Fields are each field's former values, newest first, by the field's
	// JSON pointer. A secret field's are left out.
	Fields map[string][]FormerMessage `json:"fields"`
}

// sources are the functions a store's privacy names — its retention's
// instants, its HeldUntil, its Anonymise —, for Files.
func (pv *StorePrivacyMessage) sources() []*SourceMessage {
	out := []*SourceMessage{pv.Anonymise}
	for _, r := range []*RetentionSpec{pv.Erase, pv.Delete} {
		if r != nil {
			out = append(out, r.Since, r.At)
		}
	}
	if pv.HeldUntil != nil {
		out = append(out, pv.HeldUntil.At)
	}
	return out
}

// structure is what a store's privacy says of the product's structure, for
// the revision: without what a run counts — held, due, erased, deleted.
func (pv *StorePrivacyMessage) structure() *StorePrivacyMessage {
	if pv == nil {
		return nil
	}
	cp := *pv
	cp.Held, cp.Due, cp.Erased, cp.Deleted = nil, nil, nil, nil
	return &cp
}
