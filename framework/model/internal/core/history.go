// What a store remembers: its history, the former values of a field, and a
// record's past.

package core

import (
	"encoding/json"
	"time"
)

// History (ADR 0007): what a store remembers of its records — the former
// values of the fields tagged history=N, and the password policies that
// read them — and a field's former values, as Store.Former and app.Export
// give them.

// StoreHistoryMessage is what a store remembers of its records.
// Fields names the fields it keeps former values of; Kept and Bytes weigh
// them, and Versions, Trashed and VersionBytes weigh its records' versions.
type StoreHistoryMessage struct {
	// Revisions is how many former versions each record keeps
	// (app.Revisions, ADR 0007 §3); zero when it keeps none.
	Revisions int `json:"revisions,omitempty"`
	// Fields are the JSON pointers of the fields that keep their former
	// values: tagged history=N — the schema's Field.History says how many —
	// or read by a password policy that refuses former passwords.
	Fields []string `json:"fields,omitempty"`
	// Passwords are the store's password policies.
	Passwords []PasswordPolicyMessage `json:"passwords,omitempty"`
	// Kept is how many former values the store keeps, and Bytes what its
	// histories weigh, on a runtime graph.
	Kept  *int   `json:"kept,omitempty"`
	Bytes *int64 `json:"bytes,omitempty"`
	// Versions is how many former versions the store's records keep,
	// Trashed how many records are in its trash, and VersionBytes what both
	// weigh — the JSON they hold —, on a runtime graph of a store that keeps
	// revisions (ADR 0007 §5), as the SDK's document store counts them. A
	// record's current version is the record, and counts in none of them.
	Versions     *int   `json:"versions,omitempty"`
	Trashed      *int   `json:"trashed,omitempty"`
	VersionBytes *int64 `json:"versionBytes,omitempty"`
}

// PasswordPolicyMessage is the policy of one secret field that holds a password's
// hash (Store.Passwords).
type PasswordPolicyMessage struct {
	// Field is the field's JSON pointer: "/password".
	Field string `json:"field"`
	// MinLength is the fewest characters a password may have.
	MinLength int `json:"minLength"`
	// NotReused is how many former passwords it refuses; zero when it
	// refuses none.
	NotReused int `json:"notReused,omitempty"`
	// NotCommon says it refuses the most common passwords (app.NotCommon),
	// as every policy does.
	NotCommon bool `json:"notCommon,omitempty"`
	// Source is where the policy is declared.
	Source *SourceMessage `json:"source,omitempty"`
}

// FormerMessage is one former value of a field that keeps its history: what it
// was, until when, and who replaced it.
type FormerMessage struct {
	// Value is the field's value then, as JSON; absent for a secret field,
	// whose former values are never given.
	Value json.RawMessage `json:"value,omitempty" app:"personal"`
	// Until is when it was replaced, on the app's clock.
	Until time.Time `json:"until"`
	// By is the user who replaced it — app.UserID —, empty for a write
	// with no user: a job's, a loop's, kit's own.
	By string `json:"by,omitempty"`
}

// RecordHistoryMessage is what kit remembers of one record, as the Studio's data
// view shows it (GET /_app/api/former): each field's former values, newest
// first, by the field's JSON pointer — a secret field's without their
// values, a personal or special one's redacted.
type RecordHistoryMessage struct {
	Fields map[string][]FormerMessage `json:"fields"`
}

// RecordVersionMessage is one version of a record whose store keeps
// revisions (ADR 0007 §3): its number — from 1, never given twice while the
// record exists —, when the write that made it ran — zero for a record
// stored before its store kept revisions —, who made it and which command,
// and the record as it was.
type RecordVersionMessage struct {
	Number uint64 `json:"number"`
	// At is when the write that made it ran, on the app's clock.
	At time.Time `json:"at,omitzero"`
	// By is the user who made it — app.UserID —, empty for a write with no
	// user.
	By string `json:"by,omitempty"`
	// Command is the node ID of the command that made it, empty outside
	// one.
	Command string `json:"command,omitempty"`
	// Value is the record as it was: in an export, its secret members left
	// out; in the Studio, as the data browser shows a record.
	Value json.RawMessage `json:"value,omitempty" app:"personal"`
}

// EditMessage is one change between two versions of a record, in RFC 6902's
// words: Op is add, remove or replace, Path the RFC 6901 JSON pointer of the
// member it changes, From its value before and To its value after, as JSON.
// A member whose value is not given — a secret's, or in the Studio a
// personal or special one — says it changed, never what: From and To are
// absent.
type EditMessage struct {
	Op   string          `json:"op"`
	Path string          `json:"path"`
	From json.RawMessage `json:"from,omitempty" app:"personal"`
	To   json.RawMessage `json:"to,omitempty" app:"personal"`
}

// RecordVersionsMessage is a record's versions as the Studio's data view
// shows them (GET /_app/api/revisions): newest first, each as the data
// browser shows a record — a member sealed at rest sealed, a personal,
// special or secret one redacted —, and, when two were asked for, what
// changed from one to the other, a value given only where the data browser
// would show it.
type RecordVersionsMessage struct {
	Versions []RecordVersionMessage `json:"versions"`
	// From and To are the two versions compared, when asked for; Edits what
	// changed between them.
	From  uint64        `json:"from,omitempty"`
	To    uint64        `json:"to,omitempty"`
	Edits []EditMessage `json:"edits,omitempty"`
}

// TrashedRecordMessage is one record in a store's trash as the Studio lists
// it (GET /_app/api/trash, ADR 0178 C4): its key, when the product deleted
// it and when kit purges it, on the app's clock — never its value. A key
// that reads a personal, special or secret member — the subject included —
// is not given, as the data browser never shows that member: Hidden says so.
type TrashedRecordMessage struct {
	// Key is the record's key; empty when Hidden.
	Key string `json:"key,omitempty"`
	// Hidden says the key reads a personal, special or secret member, or
	// the record no longer opens to tell: the Studio is not given it.
	Hidden bool `json:"hidden,omitempty"`
	// At is when the product deleted it.
	At time.Time `json:"at"`
	// PurgeAt is when kit purges it: its trash's length after At, or its
	// retention's instant when that comes first.
	PurgeAt time.Time `json:"purgeAt"`
}

// StoreTrashMessage is a page of a store's trash as the Studio lists it (GET
// /_app/api/trash?store=&offset=&limit=): Total records in the trash, in
// key order, Records those from the page's offset, Next the offset of the
// page after — absent on the last. A page is bounded, 200 records unless
// the Studio asks from 1 to 1000; an offset counts records, never quotes a
// key.
type StoreTrashMessage struct {
	// Store is the store's node ID.
	Store   string                 `json:"store"`
	Total   int                    `json:"total"`
	Records []TrashedRecordMessage `json:"records"`
	Next    int                    `json:"next,omitempty"`
}

// sources are the declarations a store's history names — its password
// policies —, for Files.
func (h *StoreHistoryMessage) sources() []*SourceMessage {
	if h == nil {
		return nil
	}
	out := make([]*SourceMessage, 0, len(h.Passwords))
	for i := range h.Passwords {
		out = append(out, h.Passwords[i].Source)
	}
	return out
}

// structure is what a store's history says of the product's structure, for
// the revision: without what a run counts — kept, bytes, and what the
// versions weigh.
func (h *StoreHistoryMessage) structure() *StoreHistoryMessage {
	if h == nil {
		return nil
	}
	cp := *h
	cp.Kept, cp.Bytes = nil, nil
	cp.Versions, cp.Trashed, cp.VersionBytes = nil, nil, nil
	return &cp
}
