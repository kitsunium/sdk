// Package docstore — range 0.3.80.* (ADR 0110 service/docstore block).
package docstore

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.80.0 - 0.3.80.255

// CodeDocumentNotFound identifies a read, an update, a replacement or a
// deletion of a key the store holds no document under, and a unique-index
// lookup that finds nobody.
const CodeDocumentNotFound errs.Code = 0x00_03_50_01 // 0.3.80.1

// CodeDocumentExists identifies an insertion under a key the store already
// holds a document under.
const CodeDocumentExists errs.Code = 0x00_03_50_02 // 0.3.80.2

// CodeUniqueKeyTaken identifies a write a unique index refuses: another
// document already holds one of its keys in that index.
const CodeUniqueKeyTaken errs.Code = 0x00_03_50_03 // 0.3.80.3

// CodeDocumentKeyEmpty identifies a document whose key function returned the
// empty string.
const CodeDocumentKeyEmpty errs.Code = 0x00_03_50_04 // 0.3.80.4

// CodeDocumentKeyChanged identifies an update whose function changed the key
// of the document it was given.
const CodeDocumentKeyChanged errs.Code = 0x00_03_50_05 // 0.3.80.5

// CodeIndexUnknown identifies a lookup in an index the store never declared.
const CodeIndexUnknown errs.Code = 0x00_03_50_06 // 0.3.80.6

// CodeIndexNotUnique identifies a single-document lookup in an index that may
// file several documents under one key.
const CodeIndexNotUnique errs.Code = 0x00_03_50_07 // 0.3.80.7

// CodeDocumentUndecodable identifies a stored document that no longer decodes
// into the store's type.
const CodeDocumentUndecodable errs.Code = 0x00_03_50_08 // 0.3.80.8

// CodeDocumentUnencodable identifies a value encoding/json cannot encode.
const CodeDocumentUnencodable errs.Code = 0x00_03_50_09 // 0.3.80.9

// CodePersistFailed identifies a write the filesystem did not make durable.
const CodePersistFailed errs.Code = 0x00_03_50_0A // 0.3.80.10

// CodeLoadFailed identifies a store whose files could not be read or are not
// a store's files.
const CodeLoadFailed errs.Code = 0x00_03_50_0B // 0.3.80.11

// CodeIndexBroken identifies stored documents that break a unique index, or
// an index key function that panicked while the store was opening.
const CodeIndexBroken errs.Code = 0x00_03_50_0C // 0.3.80.12

// CodeStoreClosed identifies a call on a store that has been closed.
const CodeStoreClosed errs.Code = 0x00_03_50_0D // 0.3.80.13

// CodeStoreMisconfigured identifies a configuration no store could honour.
const CodeStoreMisconfigured errs.Code = 0x00_03_50_0E // 0.3.80.14

// CodeWriteUnconfirmed identifies a write the filesystem published but could
// not confirm durable: the directory flush after the rename failed. The write
// took effect — every reader of the files sees it, and so does the store.
const CodeWriteUnconfirmed errs.Code = 0x00_03_50_0F // 0.3.80.15
