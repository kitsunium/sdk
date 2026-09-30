// What a store remembers: its history, the former values of a field, and a
// record's past.

package core

import (
	"encoding/json"
	"time"
)

// History (ADR 0007): what a store remembers of its records — the former
// values of the fields tagged history=N, and the password policies that
// read them — and a field's former values, as Store.Former and kit.Export
// give them.

// StoreHistoryMessage is what a store remembers of its records.
// Fields names the fields it keeps former values of; Kept and Bytes weigh
// them.
type StoreHistoryMessage struct {
	// Revisions is how many former versions each record keeps
	// (kit.Revisions, ADR 0007 §3); zero when it keeps none.
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
	// NotCommon says it refuses the most common passwords (kit.NotCommon),
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
	Value json.RawMessage `json:"value,omitempty" kit:"personal"`
	// Until is when it was replaced, on the app's clock.
	Until time.Time `json:"until"`
	// By is the user who replaced it — kit.UserID —, empty for a write
	// with no user: a job's, a loop's, kit's own.
	By string `json:"by,omitempty"`
}

// RecordHistoryMessage is what kit remembers of one record, as the Studio's data
// view shows it (GET /_kit/api/former): each field's former values, newest
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
	// By is the user who made it — kit.UserID —, empty for a write with no
	// user.
	By string `json:"by,omitempty"`
	// Command is the node ID of the command that made it, empty outside
	// one.
	Command string `json:"command,omitempty"`
	// Value is the record as it was: in an export, its secret members left
	// out; in the Studio, as the data browser shows a record.
	Value json.RawMessage `json:"value,omitempty" kit:"personal"`
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
	From json.RawMessage `json:"from,omitempty" kit:"personal"`
	To   json.RawMessage `json:"to,omitempty" kit:"personal"`
}

// RecordVersionsMessage is a record's versions as the Studio's data view
// shows them (GET /_kit/api/revisions): newest first, each as the data
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
// the revision: without what a run counts — kept, bytes.
func (h *StoreHistoryMessage) structure() *StoreHistoryMessage {
	if h == nil {
		return nil
	}
	cp := *h
	cp.Kept, cp.Bytes = nil, nil
	return &cp
}
