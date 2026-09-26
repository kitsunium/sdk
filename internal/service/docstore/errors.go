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

var (
	// DocumentNotFound is a miss: no document under that key, or no document
	// holding that key in a unique index.
	DocumentNotFound = errs.Define(CodeDocumentNotFound, "DOCUMENT_NOT_FOUND",
		"No document has this key",
		"service/docstore: the store holds no document under the key, or the unique index files nobody under it; the fields name the store and the index, never the key",
		errs.WithHTTPStatus(http.StatusNotFound))

	// DocumentExists refuses an insertion under a key already taken.
	DocumentExists = errs.Define(CodeDocumentExists, "DOCUMENT_EXISTS",
		"A document already has this key",
		"service/docstore: Insert refuses a key the store already holds; Put replaces, Insert never does",
		errs.WithHTTPStatus(http.StatusConflict))

	// UniqueKeyTaken refuses a write a unique index cannot file: another
	// document already holds one of its keys in that index. Nothing changed.
	UniqueKeyTaken = errs.Define(CodeUniqueKeyTaken, "UNIQUE_KEY_TAKEN",
		"Another document already has this key in a unique index",
		"service/docstore: the write was refused before anything changed; the index field names the unique index, never the key",
		errs.WithHTTPStatus(http.StatusConflict))

	// DocumentKeyEmpty refuses a document whose key is the empty string.
	DocumentKeyEmpty = errs.Define(CodeDocumentKeyEmpty, "DOCUMENT_KEY_EMPTY",
		"A document's key cannot be empty",
		"service/docstore: the store's key function returned the empty string",
		errs.WithHTTPStatus(http.StatusBadRequest))

	// DocumentKeyChanged refuses an update whose function changed the key of
	// the document it was handed. Nothing changed.
	DocumentKeyChanged = errs.Define(CodeDocumentKeyChanged, "DOCUMENT_KEY_CHANGED",
		"An update cannot change a document's key",
		"service/docstore: the update function returned a document whose key differs from the one it was given; delete and insert to rename",
		errs.WithHTTPStatus(http.StatusBadRequest))

	// IndexUnknown refuses a lookup in an index the store never declared.
	IndexUnknown = errs.Define(CodeIndexUnknown, "INDEX_UNKNOWN",
		"The store has no index of this name",
		"service/docstore: Lookup or Find named an index absent from the store's Config; the index field names it",
		errs.WithHTTPStatus(http.StatusBadRequest))

	// IndexNotUnique refuses a single-document lookup in an index that may
	// file several documents under one key.
	IndexNotUnique = errs.Define(CodeIndexNotUnique, "INDEX_NOT_UNIQUE",
		"The index is not unique: read it with Find",
		"service/docstore: Lookup returns one document and was given a multi-valued index; the index field names it",
		errs.WithHTTPStatus(http.StatusBadRequest))

	// DocumentUndecodable reports a stored document that no longer fits the
	// store's type — the type changed under data an older program wrote. The
	// decoder's own error travels as a field, never as the origin.
	DocumentUndecodable = errs.Define(CodeDocumentUndecodable, "DOCUMENT_UNDECODABLE",
		"A stored document does not match its type",
		"service/docstore: encoding/json could not decode a stored document into the store's type; the fields name the store and the cause",
		errs.WithHTTPStatus(http.StatusInternalServerError), errs.WithExitCode(exitDataErr))

	// DocumentUnencodable refuses a value encoding/json cannot encode — a
	// channel, a function, a cycle. Nothing changed.
	DocumentUnencodable = errs.Define(CodeDocumentUnencodable, "DOCUMENT_UNENCODABLE",
		"The document cannot be encoded",
		"service/docstore: encoding/json refused the value; the fields name the store and the cause",
		errs.WithHTTPStatus(http.StatusInternalServerError))

	// PersistFailed reports a write the filesystem did not make durable. The
	// store is unchanged: memory and disk still agree on the previous state.
	PersistFailed = errs.Define(CodePersistFailed, "PERSIST_FAILED",
		"The document could not be written to storage",
		"service/docstore: the filesystem refused a publication; nothing changed in memory, and the fields name the store and the step",
		errs.WithHTTPStatus(http.StatusInternalServerError), errs.WithExitCode(exitIOErr))

	// WriteUnconfirmed reports a write the filesystem published but could
	// not confirm durable — vfs's DirectorySyncFailed: the rename happened,
	// the flush of the directory that records it did not. It is not
	// PersistFailed, because the write TOOK EFFECT: the file shows it, so the
	// store applies it too rather than disagree with its own files. What is
	// in doubt is only whether it survives a power loss.
	WriteUnconfirmed = errs.Define(CodeWriteUnconfirmed, "WRITE_UNCONFIRMED",
		"The document was stored, but its durability could not be confirmed",
		"service/docstore: the publication's rename succeeded and the directory flush failed; the write stands in memory and on disk, and may not survive a power loss",
		errs.WithHTTPStatus(http.StatusInternalServerError), errs.WithExitCode(exitIOErr))

	// LoadFailed refuses to open a store whose files cannot be read, or are
	// not a store's files.
	LoadFailed = errs.Define(CodeLoadFailed, "LOAD_FAILED",
		"The store's files could not be loaded",
		"service/docstore: Open could not read or parse the snapshot or an overlay entry; the fields name the file and the problem",
		errs.WithHTTPStatus(http.StatusInternalServerError), errs.WithExitCode(exitDataErr))

	// IndexBroken refuses to open a store whose documents break a unique
	// index — the index was added after the data, or a file was edited — or
	// whose index key function panicked. A unique index that does not hold
	// would be a lie every Lookup tells.
	IndexBroken = errs.Define(CodeIndexBroken, "INDEX_BROKEN",
		"The stored documents break a unique index",
		"service/docstore: two stored documents share a key in a unique index, or a key function panicked while the indexes were rebuilt; the fields name the index",
		errs.WithHTTPStatus(http.StatusInternalServerError), errs.WithExitCode(exitDataErr))

	// StoreClosed refuses a call on a store that has been closed.
	StoreClosed = errs.Define(CodeStoreClosed, "STORE_CLOSED",
		"The store is closed",
		"service/docstore: Close was called; open the store again to use it",
		errs.WithHTTPStatus(http.StatusServiceUnavailable))

	// StoreMisconfigured refuses a configuration no store could honour: no
	// key function, an index without a name, a function or a unique one, a
	// filesystem without a path.
	StoreMisconfigured = errs.Define(CodeStoreMisconfigured, "STORE_MISCONFIGURED",
		"The document store cannot be opened as configured",
		"service/docstore: Open refused its Config; the fields name the setting and the problem",
		errs.WithExitCode(exitConfig))
)
