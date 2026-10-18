package app

import "slices"

// The labels of the references, re-made under the framework's name (ADR
// 0180 step 5c, Q6). A reference is an HMAC under a subkey of index-key, and
// the subkey is derived under a label: a former release derived every
// subkey under kit/v1 labels, and that label is in every reference it kept —
// a data key's name in the store of data keys and in the header of every box
// it sealed, the subject index, a hold's key, a database's index rows — and
// in every reference its privacy journal recorded.
//
// Step 5c-1 closed the journal a former release kept and opened the next,
// whose references are made under presentLabels (journal.go). Step 5c-2 makes
// every other reference under presentLabels too, and binds what the privacy
// service's own stores seal to their own ids, app.privacy/… — no longer
// kit.privacy/… (sealedAs). What a former release kept is re-sealed by a pass
// at the start (renamed_refs.go); until it is, and for whatever the pass
// cannot reach, it is read where it lies:
//
//   - a box names the data key it was sealed under, so it opens under
//     whichever reference names that key; a box of the privacy service's
//     stores opens under its store's id, then under kit.privacy's
//     (openMember);
//   - a hold is looked up under its record's present reference, then its
//     former one (holdOf), and a person's two references are both theirs
//     (subjectRefs);
//   - the subject index and a database's index rows are made again under the
//     present labels when the store opens: the index is rebuilt from the
//     records, and a database's rows are filed again because their
//     fingerprint changed (indexKeyID).
//
// Nothing is written under a former reference. An erasure destroys both of a
// person's keys, and both of a record's own (personRefs, ownRefs): what a
// former release sealed under one of them stops opening at once, wherever
// it lies, as what this release seals under the other.
//
// What the former journal needs to verify is not a label: its chain is over
// each entry as written. It needs its reasons opened, sealed bound to
// kit.privacy/store/journal (openMember): that read stays as long as an
// entry of a former journal is kept — until a retention of the journal,
// which ADR 0178 defers, has destroyed the key of the last month it sealed
// under. formerLabels go with the other former names in the minor that ends
// Q12's window.

var (
	// presentLabels are the labels every reference is made under.
	presentLabels = referenceLabels{
		subject: frameworkName + "/v1 subject reference",
		record:  frameworkName + "/v1 record reference",
		index:   frameworkName + "/v1 database index key",
	}
	// formerLabels are the labels a former release made every reference
	// under: read for the window, never written.
	formerLabels = referenceLabels{
		subject: formerName + "/v1 subject reference",
		record:  formerName + "/v1 record reference",
		index:   formerName + "/v1 database index key",
	}
)

// referenceLabels are the HKDF labels of index-key's three subkeys: one per
// kind of reference, so that a subject's and a record's never meet, and one
// for the index keys a store on a database files (ADR 0004).
type referenceLabels struct {
	subject, record, index string
}

// personRefs are the references of a person's data key: the present one,
// then the one a former release made. An erasure destroys both.
func (k *derivedKeys) personRefs(identity string) []string {
	return distinct(k.present.personRef(identity), k.former.personRef(identity))
}

// ownRefs are the references of the own data key of the record under key in
// the store id: the present one, then the one a former release made — named
// after kit.privacy's id for the privacy service's stores.
func (k *derivedKeys) ownRefs(id, key string) []string {
	return distinct(k.present.ownRef(id, key), k.former.ownRef(sealedAs(id), key))
}

// subjectRefs are a person's two references, present then former: a hold a
// former release placed names the second.
func (k *derivedKeys) subjectRefs(identity string) []string {
	return distinct(k.present.subjectRef(identity), k.former.subjectRef(identity))
}

// distinct is refs without a repeat, in order.
func distinct(refs ...string) []string {
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		if !slices.Contains(out, r) {
			out = append(out, r)
		}
	}
	return out
}
