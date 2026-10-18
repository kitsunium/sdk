package plug

// An engine's refusals, in kit's terms (ADR 0179 §2): the store subsystem
// translates the SDK's sentinels into a [Refusal] before they reach the core,
// which words each kind for the product and never imports the SDK's
// document store or sql to match one. The error the engine met stays inside
// the refusal, for the logs and the fields kit reads (an index's name, a
// migration's version).

// The kinds of refusal.
const (
	// RefusalNone is no refusal the framework words.
	RefusalNone Refusal = iota
	// RefusalNotFound is no document under the key, or under the index's
	// key.
	RefusalNotFound
	// RefusalExists is a document already under the key an insertion names.
	RefusalExists
	// RefusalUniqueTaken is another document under a unique index's key.
	RefusalUniqueTaken
	// RefusalKeyEmpty is a document whose key is empty.
	RefusalKeyEmpty
	// RefusalKeyChanged is an update that changes the document's key.
	RefusalKeyChanged
	// RefusalIndexUnknown is an index the store does not declare.
	RefusalIndexUnknown
	// RefusalIndexNotUnique is a lookup on an index that is not unique.
	RefusalIndexNotUnique
	// RefusalIndexBroken is stored documents that break a unique index.
	RefusalIndexBroken
	// RefusalUndecodable is a record that does not decode into its type.
	RefusalUndecodable
	// RefusalUnencodable is an entity that does not encode.
	RefusalUnencodable
	// RefusalPersist is a write the store could not keep.
	RefusalPersist
	// RefusalLoad is a store that could not be loaded; its "problem" field
	// says why.
	RefusalLoad
	// RefusalClosed is a call on a closed engine.
	RefusalClosed
	// RefusalUnconfirmed is a write that stands but whose survival across a
	// power loss is in doubt.
	RefusalUnconfirmed
	// RefusalVersionsNotKept is a call on the versions of a store that keeps
	// none.
	RefusalVersionsNotKept
	// RefusalVersionsRewrite is a rewrite of versions that adds or
	// renumbers one.
	RefusalVersionsRewrite
	// RefusalNotTrashed is no record in the trash under the key.
	RefusalNotTrashed
	// RefusalKeyTooLong is a key longer than a database's key columns hold;
	// its "max" field says how long they hold.
	RefusalKeyTooLong
	// RefusalStatement is a statement the database did not complete.
	RefusalStatement
	// RefusalTxClosed is a call with the context of a transaction that
	// ended.
	RefusalTxClosed
	// RefusalTxFailed is a transaction or a savepoint the database would
	// not open, commit or roll back.
	RefusalTxFailed
	// RefusalOutOfOrder is a migration older than one already applied; its
	// "applied" field names the newer.
	RefusalOutOfOrder
	// RefusalRenameTarget is a table whose present name is taken while its
	// former one still holds the framework's data (ADR 0180 step 5); its
	// "tables" field names them, and nothing was moved.
	RefusalRenameTarget
)

// Refusal is a kind of refusal of a store's engine or a database, as the framework
// words it.
type Refusal uint8

// RefusalError is an engine's refusal of one kind: the error it met, and
// the kind the framework words it by.
type RefusalError struct {
	// Err is what the engine met.
	Err error
	// Kind is how the framework words it.
	Kind Refusal
}

// Refuse is err as a refusal of kind.
func Refuse(kind Refusal, err error) error {
	return &RefusalError{Kind: kind, Err: err}
}

// Refused reports whether err, or an error it wraps, is a refusal of kind.
func Refused(err error, kind Refusal) bool {
	for err != nil {
		if r, ok := err.(*RefusalError); ok && r.Kind == kind {
			return true
		}
		switch u := err.(type) {
		case interface{ Unwrap() []error }:
			for _, e := range u.Unwrap() {
				if Refused(e, kind) {
					return true
				}
			}
			return false
		case interface{ Unwrap() error }:
			err = u.Unwrap()
		default:
			return false
		}
	}
	return false
}

// Error is what the engine met's.
func (e *RefusalError) Error() string {
	if e.Err == nil {
		return "app: a store's engine refused the call"
	}
	return e.Err.Error()
}

// Unwrap is what the engine met.
func (e *RefusalError) Unwrap() error { return e.Err }
