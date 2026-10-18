package app

// The labels of the references and the privacy journal, re-made under the
// framework's name (ADR 0180 step 5c, Q6). A reference is an HMAC under a
// subkey of index-key, and the subkey is derived under a label: a former
// release derived every subkey under kit/v1 labels, and this name is in
// every reference the framework keeps — a data key's name in the store of
// data keys and in the header of every box it sealed, the subject index, a
// hold's key, a database's index rows — and in every reference the privacy
// journal records. The journal's chain hashes those references, so it is
// never rewritten: the journal a former release kept is closed as it is,
// its last entry kept, and the next entry the framework journals is preceded
// by a JournalContinue entry that names that last entry's hash and opens a
// new chain (journal.go). Every entry of the new journal names its records
// and subjects under the app/v1 labels, and seals its reason bound to the
// privacy journal's own id, app.privacy/store/journal.
//
// Step 5c is two changes. This one (5c-1) closes the journal and opens the
// next; the references at rest — the data keys and their boxes, the subject
// index, the holds, a database's index rows, and the privacy service's ids a
// sealed value of its holds or a record's own data key is bound to — keep
// the kit/v1 labels and kit.privacy (sealedAs) until 5c-2 re-seals them, by
// a migration of their own. Until then a subject has two references: the
// one the store keeps, under kit/v1, and the one the new journal records,
// under app/v1.
//
// What the former journal needs to verify is not a label: its chain is over
// each entry as written, its references among the bytes hashed. What it
// needs is its reasons opened, sealed bound to kit.privacy/store/journal:
// openMember reads a box of the journal under the present id, then under the
// former one. That read stays as long as an entry of a former journal is
// kept — until a retention of the journal, which ADR 0178 defers, has
// destroyed the key of the last month the former journal sealed under; no
// minor removes it before that. The kit/v1 labels stay for 5c-2's window,
// and go with the other former names in the minor that ends it.

// journalID is the privacy journal's node id.
const journalID = privacyService + "/store/journal"

var (
	// presentLabels are the labels every reference is made under from ADR
	// 0180 step 5c: the new journal's, now; every other, once 5c-2 re-seals
	// them.
	presentLabels = referenceLabels{
		subject: frameworkName + "/v1 subject reference",
		record:  frameworkName + "/v1 record reference",
		index:   frameworkName + "/v1 database index key",
	}
	// formerLabels are the labels a former release made every reference
	// under, and those the framework keeps at rest still are made under
	// until ADR 0180 step 5c-2: read, and written only where 5c-2 has not
	// reached yet.
	formerLabels = referenceLabels{
		subject: formerName + "/v1 subject reference",
		record:  formerName + "/v1 record reference",
		index:   formerName + "/v1 database index key",
	}
	// restLabels are the labels the references the framework keeps at rest
	// are made under: the data keys, the subject index, the holds, a
	// database's index rows. They are the former ones until ADR 0180 step
	// 5c-2 re-seals them under presentLabels.
	restLabels = formerLabels
)

// referenceLabels are the HKDF labels of index-key's three subkeys: one per
// kind of reference, so that a subject's and a record's never meet, and one
// for the index keys a store on a database files (ADR 0004).
type referenceLabels struct {
	subject, record, index string
}

// sealedIn is what a value the store id seals now is bound to: the privacy
// journal's own id, since every entry the framework writes belongs to the
// journal it opened under its name (ADR 0180 step 5c); every other store's
// sealedAs, until 5c-2.
func sealedIn(id string) string {
	if id == journalID {
		return id
	}
	return sealedAs(id)
}
