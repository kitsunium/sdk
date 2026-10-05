// Personal data: the fields a store classifies, erasure, retention, legal
// holds and the privacy journal.

package core

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
