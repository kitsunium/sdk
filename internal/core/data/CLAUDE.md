<!-- updated: 2026-10-03T06:00:00Z -->
# internal/core/data/

## Purpose

The data family's contracts (ADR 0155): the ports, the values and the codes
of the domains that encode, store and move a caller's data. This directory
holds no Go code: it is a prefix, not a package, and nothing imports
`internal/core/data` itself. Each member is a package of the `internal/core`
module with its own `CLAUDE.md`, and each still follows the core's rules —
interfaces and immutable values, the standard library and the kernel only
(`internal/core/CLAUDE.md`). The engines are the same paths under
`internal/service/data`, and the facades the same paths under `pkg/v1/data`.

## The rule that put them together

A domain belongs here when its subject is the caller's DATA itself — the bytes
a value becomes and the places those bytes are kept — rather than what the
data is for: the bytes a value is written as (`codec`) and the same bytes made
smaller (`transform`), the tables of the caller's database (`sql`) and the
documents kept above them or on a filesystem (`docstore`), the messages one
process leaves for another (`queue`), the copies kept close (`cache`) and the
files a value is published to (`vfs`).

Every wire format read or written for its own sake is a codec and lives under
`codec/` (ADR 0155 §3); a format that exists to serve one domain — a WebSocket
frame, a `traceparent`, a language tag — stays with that domain.
`transform/` is not a codec: it transforms bytes and maps no value, so its
registry is a sibling of the codec registry and never a `Format` (ADR 0014).
A queue is not an event bus: `events`, one process and synchronous, is an
application mechanism (`internal/core/app/events`), and the frontier between the
two is ADR 0053's table.

`docstore`, the family's seventh domain, has no core package yet — its codes
are declared by its engine, `internal/service/data/docstore` (ADR 0110 §D1).
ADR 0160 §1 gives it one, as `docstore/` beside the six below, when the
reorganisation series reaches it.

## Members

| Package | What it declares | Code range | Engine |
|---|---|---|---|
| `cache/` | the `Store[V]` port frozen at three methods with `EntryFetcher[V]` / `Tagger` / `Loader[V]` as siblings, `EntryValue[V]`, the `Fill[V]` FUNC port and `NoExpiry`; no registry, because Go has no `map[Name]Store[V]` for an open `V` (ADR 0049) | `0.2.18.*` | `internal/service/data/cache` |
| `codec/` | the `Codec` / `StreamingCodec` / `Encoder` / `Decoder` / `Appender` ports, the process-wide registry and the `Format` value (ADR 0003); `codec/scratch/` beneath it holds the codec-payload recycling threshold every engine shares | `0.2.2.*` | `internal/service/data/codec/*` |
| `queue/` | the `Broker` port frozen at four methods, the `Handler` FUNC port, the message, delivery, lease, receipt and dead-letter values, `PolicyValue`, and five ADR 0039 siblings; at-least-once in the type (ADR 0054, ADR 0104, ADR 0151) | `0.2.23.*` | `internal/service/data/queue` |
| `sql/` | the `Executor` / `Transactor` / `Checker` / `Migrator` ports over the stdlib's own `*sql.Rows` and `sql.Result`, the `Preparer`, `Joiner` and `Deferrer` siblings, and the closed `Dialect` set (ADR 0055, ADR 0139) | `0.2.24.*` | `internal/service/data/sql` |
| `transform/` | the `Compressor` port and the registry mapping an `Algorithm` to it (ADR 0014) | `0.2.5.*` | `internal/service/data/transform` |
| `vfs/` | `FS`, an alias of `io/fs.FS`, `WritableFS` frozen at four write verbs, the `AtomicWriter` sibling, and the lexical path and permission guards (ADR 0056) | `0.2.25.*` | `internal/service/data/vfs` |

A code keeps its value when its package moves (ADR 0160): the six ranges
above are the ones these packages declared before the family existed, and
`codeRangeOwners` (`internal/kernel/errs/registry_ownership_external_test.go`)
names the new directories under the same keys.

## Do NOT

- Put Go code in this directory. A file here would make `data` a package of
  its own, and the family a domain nobody chose.
- File a wire format here because it is parsed: a format that serves one
  domain stays in that domain, and only a format read or written for its own
  sake is a codec (ADR 0155 §3).
- Renumber a member's codes to match its new path. Nothing derives a code from
  a directory, and a consumer branches on the value (ADR 0160).
