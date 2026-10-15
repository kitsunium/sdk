<!-- updated: 2026-10-15T00:00:00Z -->
# internal/core/data/docstore/

## Purpose

The **document-store domain**: what the two engines of
`internal/service/data/docstore` share — the file engine (`Open`, in memory or
through `core/data/vfs`, ADR 0110) and the SQL engine (`OpenSQL`, two tables of
the caller's database, ADR 0139), both keeping a document's versions in its own
write when asked (ADR 0143), and then a trash (ADR 0143, amended for ADR 0178). It holds the ports each engine implements, the
values both read and return, the declaration of a secondary index both take,
and the codes and sentinels both answer. Created by ADR 0160 §1, which gives
every service domain a core.

Code range: `0.3.80.*` — allocated by the engine's layer (`LL = 3`) and declared
here since ADR 0160, which keeps every value (§3). Public facade:
`pkg/v1/data/docstore`.

### Why it exists now, and why it holds two ports and not one

ADR 0110 §D1 decided "one engine, no port", and the values were the engine's
(ADR 0074). That stopped holding when ADR 0139 added a second engine: there
were two implementations of one contract — the same documents, keys, index
declarations, write modes, hooks and refusals under the same codes — and
nothing a caller could hold to stand a double in for either.

ADR 0139 §D2 kept "no port" for a reason that still stands: the file engine
takes no context, because it waits on nothing a caller could abandon (ADR 0110
§D6), while every call of the SQL engine waits on a database and takes one. A
single interface would give the file engine a context it cannot honour, or take
the SQL engine's away. So the port is two ports, one per shape of call, with
the same methods otherwise:

| Port | Implemented by | Calls |
|---|---|---|
| `Collection[T]` | the file engine, `*Store[T]` | no context |
| `Versioned[T]` — sibling | the file engine | no context |
| `CollectionContext[T]` | the SQL engine, `*SQLStore[T]` | a context first, on every call |
| `VersionedContext[T]` — sibling | the SQL engine | a context first, on every call |
| `Trashable` — sibling | the file engine | no context |
| `TrashableContext` — sibling | the SQL engine | a context first, on every call |
| `KeyLister` — sibling | the file engine | no context |
| `KeyListerContext` — sibling | the SQL engine | a context first |
| `VersionWriter` — sibling | the file engine | no context |
| `VersionWriterContext` — sibling | the SQL engine | a context first |
| `VersionWeigher` — sibling | the file engine | no context |
| `VersionWeigherContext` — sibling | the SQL engine | a context first |
| `Announcer` | both, exactly as it is | `OnWrite`, `OnDelete` |

The versions are a sibling and not part of the port because a store keeps them
only when its configuration says so (ADR 0143), and a double of the plain
contract should not have to answer them. The trash is a sibling for the same
reason, and of its own rather than part of `Versioned`, which is published and
frozen (ADR 0039); it is not generic, because none of its calls names a `T`.
What only one engine has stays on the engine: the file engine's `Stats`, `Fold` and `Close`, the SQL engine's
`Count` and `Reindex` — a double of the port has no files to fold and no tables
to file again.

`KeyLister` and `KeyListerContext` (ADR 0178) are the keys alone: `Keys(after)`
yields every key above `after`, in byte order, and reads no document, so what a
listing costs does not grow with what the documents weigh — the only exported
listing before them, `Entries`, copied every document (measured in ADR 0178
C6). They are not generic: a key is a string whatever `T` is, so a caller that
lists the keys of stores of several types holds one port. A sibling, not a
method of `Collection`, so a double of the plain contract need not answer it.

`VersionWriter` and `VersionWriterContext` (ADR 0178) write a document with its
versions as they are given: `PutDocument(key, versions, trashedAt)` keeps each
version's number, instant — zero for a version 1 that predates its store's
versions — and metadata, and, with `trashedAt` set, puts the document in the
trash at that instant. A copy from one engine to another needs it, and no other
write can: a write numbers the version it makes after the current one and
stamps it with the store's clock. It refuses a taken key (`DOCUMENT_EXISTS`) and
versions no store keeps as given — none, a number 0, numbers that do not
strictly decrease, an unknown instant on a version other than 1, a version that
is not JSON, a current document whose own key is another — with
`VERSIONS_INVALID` (`0.3.80.22`), its `problem` field naming which. Not generic,
for the reason `KeyLister` is not: no call names a `T`.

`VersionWeigher` and `VersionWeigherContext` (ADR 0143, the amendment for kit's
Studio) say what a store's versions weigh: `VersionWeight()` answers a
`VersionWeightValue` — `Former` versions, `Trashed` documents, and the
`Bytes` of the JSON they hold; a current version is the document and counts in
none. The file engine answers from a counter every write moves (also in its
`Stats`), the SQL engine from one aggregate statement over the versions table,
and the same writes weigh the same on both. A store that keeps no versions
answers `VERSIONS_NOT_KEPT`. Not generic, and a sibling, for the reasons above;
it reads no document, so what it costs does not grow with the documents.

**The ports are generated from the design** (ADR 0163): `Announcer`,
`Collection`, `Versioned`, `CollectionContext`, `VersionedContext`,
`KeyLister`, `KeyListerContext`, `VersionWriter`, `VersionWriterContext`,
`VersionWeigher` and `VersionWeigherContext` are declared, with their doc comments, under `ports:` in
`design/data/docstore.yaml`, and `kit gen` writes them into `design_gen.go`. A
port or its doc comment changes in the design, then `kit gen`, then `make api`
— never in `design_gen.go`, whose header digests `make api-check` verifies.
They moved there from `docstore.go` and `docstore_interface.go`, which, left
with nothing but their package comment, are gone (rule 5);
the package comment is `doc.go`'s alone, written by kit from the design (ADR 0167).

## Surface

| File | Holds |
|---|---|
| `doc.go` | the package comment — kit writes it from the design (ADR 0167) |
| `design_gen.go` | GENERATED by `kit gen` from `design/data/docstore.yaml` (ADR 0163) — `Announcer` (`OnWrite` / `OnDelete`) and `Collection[T]` — `Get`, `List`, `Filter`, `Entries`, `Lookup`, `Find`; `Put`, `Insert`, `Replace`, `Update`, `Delete`; and `Announcer` —, and the siblings: `Versioned[T]` — the four Stamped writes, `Versions`, `Version`, `RewriteVersions` — and the two context ports, `CollectionContext[T]` and `VersionedContext[T]`, the same methods each taking a context first; the trash's pair, `Trashable` — `Trash`, `Restore`, `Purge`, `Tombstones` — and `TrashableContext`; and `KeyLister` and `KeyListerContext` — `Keys(after)` / `Keys(ctx, after)`, an `iter.Seq2[string, error]`, a trashed document never listed (ADR 0178); and `VersionWriter` and `VersionWriterContext` — `PutDocument(key, versions, trashedAt)`, a document written with its versions and its trash as they are given (ADR 0178); and `VersionWeigher` and `VersionWeigherContext` — `VersionWeight()` / `VersionWeight(ctx)`, what the versions weigh (ADR 0143) |
| `value.go` | `EntryValue` (a document as JSON, with its key), `VersionValue` (`Number` from 1, `At`, `Meta`, `JSON`), `StampValue` (`Meta`, `InPlace`), `TombstoneValue` (`Key`, `At`), `VersionWeightValue` (`Former`, `Trashed`, `Bytes`) |
| `index.go` | `IndexSpec[T]` (`Keys`, `Name`, `Unique`) and its two constructors, `Unique` — one key, the empty key filed as none, a nil function kept nil for the engine to refuse by name — and `Index` |
| `codes_gen.go` | `0.3.80.1`–`0.3.80.22` and their twenty-two sentinels — written by kit gen from `design/data/docstore.yaml` (ADR 0164) |
| `errors.go` | hand-written beside them: `exitConfig`, `exitDataErr`, `exitIOErr`, `exitTempFail`, `httpBadRequest`, `httpNotFound`, `httpConflict`, `httpInternalServerError`, `httpServiceUnavailable` |
| `docstore_external_test.go` | one double per port holding exactly its methods (the ADR 0039 guard), every sentinel's code, reason, exit status and HTTP status, and the index declarations |

`internal/service/data/docstore/docstore_compliance.go` asserts at compile time
that `*Store[T]` is a `Collection[T]`, a `Versioned[T]`, a `Trashable`, a
`KeyLister`, a `VersionWriter` and a `VersionWeigher`, `*SQLStore[T]` a
`CollectionContext[T]`, a `VersionedContext[T]`, a `TrashableContext`, a
`KeyListerContext`, a `VersionWriterContext` and a `VersionWeigherContext`, and
both an `Announcer`.

## Error codes

Range `0.3.80.*`, shared by both engines — a caller that maps them maps both:

| Code | Sentinel | HTTP | Exit |
|---|---|---|---|
| `0.3.80.1` | `DocumentNotFound` | 404 | 70 |
| `0.3.80.2` | `DocumentExists` | 409 | 70 |
| `0.3.80.3` | `UniqueKeyTaken` | 409 | 70 |
| `0.3.80.4` | `DocumentKeyEmpty` | 400 | 70 |
| `0.3.80.5` | `DocumentKeyChanged` | 400 | 70 |
| `0.3.80.6` | `IndexUnknown` | 400 | 70 |
| `0.3.80.7` | `IndexNotUnique` | 400 | 70 |
| `0.3.80.8` | `DocumentUndecodable` | 500 | 65 |
| `0.3.80.9` | `DocumentUnencodable` | 500 | 70 |
| `0.3.80.10` | `PersistFailed` | 500 | 74 |
| `0.3.80.11` | `LoadFailed` | 500 | 65 |
| `0.3.80.12` | `IndexBroken` | 500 | 65 |
| `0.3.80.13` | `StoreClosed` | 503 | 70 |
| `0.3.80.14` | `StoreMisconfigured` | 500 | 78 |
| `0.3.80.15` | `WriteUnconfirmed` | 500 | 74 |
| `0.3.80.16` | `StatementFailed` (SQL engine, ADR 0139) | 503 | 75 |
| `0.3.80.17` | `KeyTooLong` (SQL engine, ADR 0139) | 400 | 70 |
| `0.3.80.18` | `VersionsNotKept` (ADR 0143) | 400 | 70 |
| `0.3.80.19` | `VersionNotFound` (ADR 0143) | 404 | 70 |
| `0.3.80.20` | `VersionsRewriteRefused` (ADR 0143) | 400 | 70 |
| `0.3.80.21` | `DocumentNotTrashed` (ADR 0143, amended for ADR 0178) | 404 | 70 |
| `0.3.80.22` | `VersionsInvalid` (ADR 0143, amended for ADR 0178's `data copy`) | 400 | 70 |

The HTTP statuses are integers named after their status (`httpNotFound int =
404`, …) rather than `net/http` constants: a core package does not import
`net/http` to read five numbers (ADR 0160). The Privates still begin
`service/data/docstore:`, because that is where every one of them is emitted.

## Do NOT

- **Add a method to a port.** `pkg/v1/data/docstore` aliases every one, Go
  interfaces are structural, and every double a consumer wrote would stop
  compiling. A capability grows as a sibling (ADR 0039), as `Versioned` and `Trashable` did;
  `TestDoublesStillSatisfyThePorts` fails on anyone who folds one in.
- **Merge the two families into one port.** That is the context an engine
  cannot honour, or the one it cannot lose (ADR 0139 §D2).
- **Put an engine's own call on a port** — `Stats`, `Fold`, `Close`, `Count`,
  `Reindex` — nor an engine's configuration here: `Config` and `SQLConfig`
  belong to their engines (ADR 0074), and so does the file engine's
  `StatsValue`.
- **Put a statement, a file format or an encoding here.** The engines' SQL, the
  overlay and the snapshot are mechanism and live in the service (ADR 0160 §4).
- **Quote a key, an index key or a document** in a sentinel's Public or
  Private: a store key is routinely an e-mail address, an index key the hash of
  a token.
- **Renumber a code** to match the layer it is declared in. `LL = 3` records
  the layer that allocated the range (ADR 0160 §3).
- **Import `net/http`** for a status constant.

## Verification

```
cd internal/core && GOWORK=off go test -race ./data/docstore/
bazel test --config=race //internal/core/data/docstore:docstore_test
```
