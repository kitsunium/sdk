// Package docstore — declares the sentinel *errs.Error outcomes. Each var's
// name equals its errs.Define Reason in SCREAMING_SNAKE form.
//
// No Public and no Private below carries a key or a document. A store key is
// routinely an e-mail address or the hash of a token, and an index key is
// usually exactly the value a caller must not learn back from a refusal: the
// fields name the index and the operation, never what was looked up.
package docstore

import (
	"net/http"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// exitConfig matches sysexits EX_CONFIG (78): the store was wired wrong.
const exitConfig int = 78

// exitDataErr matches sysexits EX_DATAERR (65): the stored data is wrong.
const exitDataErr int = 65

// exitIOErr matches sysexits EX_IOERR (74): the filesystem failed.
const exitIOErr int = 74

// exitTempFail matches sysexits EX_TEMPFAIL (75): the database did not answer,
// and the same call may well succeed once it does.
const exitTempFail int = 75

var (
	// DocumentNotFound is a miss: no document under that key, or no document
	// holding that key in a unique index.
	DocumentNotFound = errs.Define(CodeDocumentNotFound, "DOCUMENT_NOT_FOUND",
		"No document has this key",
		"service/data/docstore: the store holds no document under the key, or the unique index files nobody under it; the fields name the store and the index, never the key",
		errs.WithHTTPStatus(http.StatusNotFound))

	// DocumentExists refuses an insertion under a key already taken.
	DocumentExists = errs.Define(CodeDocumentExists, "DOCUMENT_EXISTS",
		"A document already has this key",
		"service/data/docstore: Insert refuses a key the store already holds; Put replaces, Insert never does",
		errs.WithHTTPStatus(http.StatusConflict))

	// UniqueKeyTaken refuses a write a unique index cannot file: another
	// document already holds one of its keys in that index. Nothing changed.
	UniqueKeyTaken = errs.Define(CodeUniqueKeyTaken, "UNIQUE_KEY_TAKEN",
		"Another document already has this key in a unique index",
		"service/data/docstore: the write was refused before anything changed; the index field names the unique index, never the key",
		errs.WithHTTPStatus(http.StatusConflict))

	// DocumentKeyEmpty refuses a document whose key is the empty string.
	DocumentKeyEmpty = errs.Define(CodeDocumentKeyEmpty, "DOCUMENT_KEY_EMPTY",
		"A document's key cannot be empty",
		"service/data/docstore: the store's key function returned the empty string",
		errs.WithHTTPStatus(http.StatusBadRequest))

	// DocumentKeyChanged refuses an update whose function changed the key of
	// the document it was handed. Nothing changed.
	DocumentKeyChanged = errs.Define(CodeDocumentKeyChanged, "DOCUMENT_KEY_CHANGED",
		"An update cannot change a document's key",
		"service/data/docstore: the update function returned a document whose key differs from the one it was given; delete and insert to rename",
		errs.WithHTTPStatus(http.StatusBadRequest))

	// IndexUnknown refuses a lookup in an index the store never declared.
	IndexUnknown = errs.Define(CodeIndexUnknown, "INDEX_UNKNOWN",
		"The store has no index of this name",
		"service/data/docstore: Lookup or Find named an index absent from the store's Config; the index field names it",
		errs.WithHTTPStatus(http.StatusBadRequest))

	// IndexNotUnique refuses a single-document lookup in an index that may
	// file several documents under one key.
	IndexNotUnique = errs.Define(CodeIndexNotUnique, "INDEX_NOT_UNIQUE",
		"The index is not unique: read it with Find",
		"service/data/docstore: Lookup returns one document and was given a multi-valued index; the index field names it",
		errs.WithHTTPStatus(http.StatusBadRequest))

	// DocumentUndecodable reports a stored document that no longer fits the
	// store's type — the type changed under data an older program wrote. The
	// decoder's own error travels as a field, never as the origin.
	DocumentUndecodable = errs.Define(CodeDocumentUndecodable, "DOCUMENT_UNDECODABLE",
		"A stored document does not match its type",
		"service/data/docstore: encoding/json could not decode a stored document into the store's type; the fields name the store and the cause",
		errs.WithHTTPStatus(http.StatusInternalServerError), errs.WithExitCode(exitDataErr))

	// DocumentUnencodable refuses a value encoding/json cannot encode — a
	// channel, a function, a cycle. Nothing changed.
	DocumentUnencodable = errs.Define(CodeDocumentUnencodable, "DOCUMENT_UNENCODABLE",
		"The document cannot be encoded",
		"service/data/docstore: encoding/json refused the value; the fields name the store and the cause",
		errs.WithHTTPStatus(http.StatusInternalServerError))

	// PersistFailed reports a write the filesystem did not make durable. The
	// store is unchanged: memory and disk still agree on the previous state.
	PersistFailed = errs.Define(CodePersistFailed, "PERSIST_FAILED",
		"The document could not be written to storage",
		"service/data/docstore: the filesystem refused a publication; nothing changed in memory, and the fields name the store and the step",
		errs.WithHTTPStatus(http.StatusInternalServerError), errs.WithExitCode(exitIOErr))

	// WriteUnconfirmed reports a write the filesystem published but could
	// not confirm durable — vfs's DirectorySyncFailed: the rename happened,
	// the flush of the directory that records it did not. It is not
	// PersistFailed, because the write TOOK EFFECT: the file shows it, so the
	// store applies it too rather than disagree with its own files. What is
	// in doubt is only whether it survives a power loss.
	WriteUnconfirmed = errs.Define(CodeWriteUnconfirmed, "WRITE_UNCONFIRMED",
		"The document was stored, but its durability could not be confirmed",
		"service/data/docstore: the publication's rename succeeded and the directory flush failed; the write stands in memory and on disk, and may not survive a power loss",
		errs.WithHTTPStatus(http.StatusInternalServerError), errs.WithExitCode(exitIOErr))

	// LoadFailed refuses to open a store whose files cannot be read, are not
	// a store's files, or keep versions the store was not opened to keep.
	LoadFailed = errs.Define(CodeLoadFailed, "LOAD_FAILED",
		"The store's files could not be loaded",
		"service/data/docstore: Open could not read or parse the snapshot, the versions file or an overlay entry, or found versions it does not keep; the fields name the file and the problem",
		errs.WithHTTPStatus(http.StatusInternalServerError), errs.WithExitCode(exitDataErr))

	// IndexBroken refuses to open a store whose documents break a unique
	// index — the index was added after the data, or a file was edited — or
	// whose index key function panicked. A unique index that does not hold
	// would be a lie every Lookup tells.
	IndexBroken = errs.Define(CodeIndexBroken, "INDEX_BROKEN",
		"The stored documents break a unique index",
		"service/data/docstore: two stored documents share a key in a unique index, or a key function panicked while the indexes were rebuilt; the fields name the index",
		errs.WithHTTPStatus(http.StatusInternalServerError), errs.WithExitCode(exitDataErr))

	// StoreClosed refuses a call on a store that has been closed.
	StoreClosed = errs.Define(CodeStoreClosed, "STORE_CLOSED",
		"The store is closed",
		"service/data/docstore: Close was called; open the store again to use it",
		errs.WithHTTPStatus(http.StatusServiceUnavailable))

	// StoreMisconfigured refuses a configuration no store could honour: no
	// key function, an index without a name, a function or a unique one, a
	// filesystem without a path, a negative Versions, a Held without
	// Versions; for the SQL store, a transactor that cannot join or defer, an
	// unknown dialect, a table name that is not one.
	StoreMisconfigured = errs.Define(CodeStoreMisconfigured, "STORE_MISCONFIGURED",
		"The document store cannot be opened as configured",
		"service/data/docstore: Open or OpenSQL refused its Config; the fields name the setting and the problem",
		errs.WithExitCode(exitConfig))

	// StatementFailed reports a call the SQL store's database did not
	// complete: it could not be reached, its context ended, or it refused a
	// statement for a reason that is none of the store's own refusals. What
	// the call was writing did not take effect.
	//
	// The driver's error travels beside it under errors.Join, reachable by
	// errors.Is and errors.As — a context's deadline included — and WITHHELD
	// from its text: a driver quotes the row a constraint refused, and that
	// row holds a key (ADR 0139).
	StatementFailed = errs.Define(CodeStatementFailed, "STATEMENT_FAILED",
		"The store's database did not complete the request",
		"service/data/docstore: a SQL statement failed; the driver's error is joined beside this one, its text withheld, and the fields name the table and the step",
		errs.WithHTTPStatus(http.StatusServiceUnavailable), errs.WithExitCode(exitTempFail))

	// KeyTooLong refuses a write to the SQL store whose store key, or one of
	// whose index keys, is longer than its key columns hold. It is refused
	// before any statement, because MySQL outside strict mode would truncate
	// the key rather than refuse it — and a truncated key is another key.
	KeyTooLong = errs.Define(CodeKeyTooLong, "KEY_TOO_LONG",
		"A key is longer than the store can keep",
		"service/data/docstore: a store key or an index key exceeds MaxSQLKeyLen bytes; the fields name the store, the index and the limit, never the key",
		errs.WithHTTPStatus(http.StatusBadRequest))

	// VersionsNotKept refuses a call on a document's versions — Versions,
	// Version, RewriteVersions — to a store whose configuration keeps none.
	// Answering the current document as its only version would tell the
	// caller a history exists and is empty, which is not what happened.
	VersionsNotKept = errs.Define(CodeVersionsNotKept, "VERSIONS_NOT_KEPT",
		"This store keeps no versions",
		"service/data/docstore: the store was opened with Versions zero, so it records no version and reads none; the fields name the store",
		errs.WithHTTPStatus(http.StatusBadRequest))

	// VersionNotFound is a miss on a version: the document holds no version of
	// that number — it was never made, or a write pruned it.
	VersionNotFound = errs.Define(CodeVersionNotFound, "VERSION_NOT_FOUND",
		"The document keeps no version of this number",
		"service/data/docstore: the document exists and keeps no version of the number asked for, never made or pruned; the fields name the store and the number, never the key",
		errs.WithHTTPStatus(http.StatusNotFound))

	// VersionsRewriteRefused refuses what a rewrite's function returned: a
	// rewrite may drop a former version and change what one holds and what
	// its writer said, never add one, reorder them or change when one was
	// made. Nothing changed.
	VersionsRewriteRefused = errs.Define(CodeVersionsRewriteRefused, "VERSIONS_REWRITE_REFUSED",
		"A rewrite of versions may only drop or rewrite the versions it was given",
		"service/data/docstore: the rewrite function returned a version it was not given, out of order, with another instant, or that is not JSON; the fields name the store and the problem",
		errs.WithHTTPStatus(http.StatusBadRequest))
)
