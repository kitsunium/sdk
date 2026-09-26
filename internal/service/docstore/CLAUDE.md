# internal/service/docstore/

## Purpose

A typed, keyed store of JSON documents with unique and multi-valued secondary
indexes, in memory or persisted through `core/vfs` — every write durable
before it returns, and a write's cost independent of how many documents the
store holds. **ADR 0110.** Public facade: `pkg/v1/docstore`.

Code range `0.3.80.*`. No core counterpart: there is one engine and no port a
second implementation would satisfy, so the values are the engine's (ADR 0074).

## Contents

| File | Surface |
|---|---|
| `docstore.go` | package doc, `Store[T]`, `EntryValue`, `StatsValue`; the reads — `Get`, `List`, `Filter`, `Entries`, `Lookup`, `Find`, `Stats`; `decode`, `jsonCause` (a decoding failure described without a byte of the document) |
| `open.go` | `Open[T](Config[T], ...IndexSpec[T])`; `newStore`; `open` — load, rebuild, THEN fold, so a refused open writes no data |
| `config.go` | `Config[T]` (`Key`, `FS`, `Path`, `FoldAt`), `IndexSpec[T]`, `Unique`, `Index`, `DefaultFoldAt`; the refusals (`StoreMisconfigured`) |
| `write.go` | `Put` / `Insert` / `Replace` (the three write modes), `Update`, `Delete`; `prepare` (outside every lock) → `commit` / `modify` / `remove` (under the writers' lock) → `persistAndApply`; `encodeCause` |
| `index.go` | the index maps (`entries`, `owned`), `uniqueTaken`, `file` / `unfile`, `rebuild` on open: every document decoded, checked against its own `Key` (`LOAD_FAILED` otherwise, naming the file via `origin`) and filed; a broken unique index or a panicking key function refuses the open |
| `persist.go` | the files: `entryName` (SHA-256 of the key), `persist` (one overlay entry per write), `publish` (PersistFailed vs WriteUnconfirmed), `maybeFold`, `Fold`, `Close`, `fold` / `removeFolded` / `recordFold`, `encodeSnapshot` |
| `load.go` | `load`: the directories, `readSnapshot`, `readOverlay` / `replay`, the crash leftovers removed; it reports whether `open` must fold, and folds nothing itself |
| `hooks.go` | `OnWrite` / `OnDelete`, called with the key after the write is durable, outside every lock |
| `codes.go` / `errors.go` | `0.3.80.1`–`0.3.80.15` |
| `BENCH.md` | what a write costs as the store grows, against the whole-file rewrite it replaces |

## Why-this-shape

- **One snapshot at rest, one overlay entry per write.** The whole-file store
  this replaces rewrote every document on every write — measured here at
  70.3 ms per write at 100 000 documents. A write now publishes ONE small file
  (the key and its document, or its deletion) and costs the same at a hundred
  documents or a hundred thousand; the overlay is folded into the snapshot when
  it holds as many entries as the store holds documents (at least
  `DefaultFoldAt`), so the fold's O(N) is paid once per N writes. `Close` folds,
  so a closed store is ONE file a person can read — the bare `{key: document}`
  object a framework's existing files already are.
- **Replay is order-free and idempotent, by construction.** An entry holds its
  key's WHOLE latest state, and the entry's name is the key's digest, so there
  is at most one entry per key. An entry the last fold already contains holds
  exactly what the snapshot holds. Hence: a crash after the snapshot and before
  the removals, or between two removals, or a removal the filesystem never made
  durable, all load the same documents — `TestAFoldInterruptedAnywhereLoadsTheSameDocuments`.
  The fold holds the writers' lock throughout, which is what makes "an entry it
  removes holds what the snapshot holds" true.
- **Two locks, and readers never wait for a disk.** `writing` serialises
  writers and is held across the filesystem work; `mu` guards the maps and is
  taken only to apply a write that is already durable. A reader sees the state
  before a write or after it — `TestReadersDoNotWaitForTheDisk` holds a
  publication at a gate and reads meanwhile.
- **Two verdicts for a failed publication.** `PERSIST_FAILED`: nothing changed,
  memory and disk agree. `WRITE_UNCONFIRMED`: vfs's `DirectorySyncFailed` —
  the rename happened, so the write TOOK EFFECT and the store applies it (the
  file shows it; a store that disagreed with its own files would be worse); only
  its survival across a power loss is in doubt. A failed automatic fold does not
  fail the write that triggered it — that write's entry was already durable — and
  is kept in `Stats().FoldError`; the entries stay until a fold works.
- **Indexes are rebuilt, never persisted.** They are derived data, and a
  derived file that could disagree with its source would need a repair path.
  A unique index the documents break refuses the OPEN (`INDEX_BROKEN`): a
  unique index that does not hold would be a lie every `Lookup` tells. The
  fold at open comes after the rebuild, so a refused open leaves the snapshot
  an operator has to repair byte for byte — `TestARefusedOpenWritesNothing`.
- **A store opens only over documents it can serve.** Every document is
  decoded at open, with or without indexes, and must sit under the key its own
  `Key` gives: a document filed under another key would be served under one
  identity while `Update` refused it as a rename. A snapshot that is JSON
  `null` is refused too — it decodes into no map without an error.
- **The key never reaches an error.** A store key is routinely an e-mail
  address; an index key routinely the hash of a token. Every refusal names the
  store and the index, never a key, and a decoding failure names the field and
  the Go type — never the offending value, whose digits encoding/json quotes.
- **No context.** Nothing here can be abandoned: the waits are the writers'
  mutex and a device flush, and a flush a caller walked away from still
  happens.

## Do NOT

- **Persist the indexes.** Rebuild them. See above.
- **Remove an overlay entry outside a fold**, or fold without the writers'
  lock: the idempotent replay rests on "every entry removed is inside the
  snapshot just written".
- **Apply a write before it is durable**, except `WRITE_UNCONFIRMED`, whose
  rename already happened.
- **Quote a key, an index key or a document** in an error or a field.
- **Run a caller's function under `mu`.** Key functions run before any lock;
  `Update`'s function runs under `writing` only, so it can read the store.

## Verification

```
bazel test --config=race //internal/service/docstore:docstore_test
# OR
cd internal/service && GOWORK=off go test -race ./docstore
```
