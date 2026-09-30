// What a store remembers: its history, the former values of a field, and a
// record's past.

package model

import (
	"github.com/kitsunium/sdk/framework/model/internal/core"
)

type (
	// StoreHistory is what a store remembers of its records.
	// Fields names the fields it keeps former values of; Kept and Bytes weigh
	// them.
	StoreHistory = core.StoreHistoryMessage
)

type (
	// PasswordPolicy is the policy of one secret field that holds a password's
	// hash (Store.Passwords).
	PasswordPolicy = core.PasswordPolicyMessage
)

type (
	// Former is one former value of a field that keeps its history: what it
	// was, until when, and who replaced it.
	Former = core.FormerMessage
)

type (
	// RecordHistory is what kit remembers of one record, as the Studio's data
	// view shows it (GET /_kit/api/former): each field's former values, newest
	// first, by the field's JSON pointer — a secret field's without their
	// values, a personal or special one's redacted.
	RecordHistory = core.RecordHistoryMessage
)

type (
	// RecordVersion is one version of a record whose store keeps revisions
	// (ADR 0007 §3): its number, when the write that made it ran, who made
	// it and which command, and the record as it was.
	RecordVersion = core.RecordVersionMessage
)

type (
	// Edit is one change between two versions of a record, in RFC 6902's
	// words: an add, a remove or a replace at a JSON pointer.
	Edit = core.EditMessage
)

type (
	// RecordVersions is a record's versions as the Studio's data view shows
	// them (GET /_kit/api/revisions), and what changed between two.
	RecordVersions = core.RecordVersionsMessage
)

type (
	// ExportedVersions are the versions of one exported record.
	ExportedVersions = core.ExportedVersionsMessage
)
