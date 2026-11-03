<!-- updated: 2026-10-03T05:20:00Z -->
# internal/service/observe/logger/internal/

## Purpose

The logger's private helpers. This directory holds no Go code: it is a prefix,
not a package. Go's `internal/` rule lets only a package under
`internal/service/observe/logger` import what sits below it — the logger, its
sinks, its middlewares and its writers — and nothing else.

## Members

| Package | What it is | Importers |
|---|---|---|
| `logfile/` | the hardened open both file sinks share: an `os.Lstat` refusal of a symlinked final component, then `O_APPEND\|O_CREATE\|O_WRONLY` plus `O_NOFOLLOW` on every Unix, both refusals naming `path` and `kind=symlink` (CWE-59). It owns no code — each sink hands it a `RefusalSpec` with its own wraps | `logger/sink/file` (`New`), `logger/writer/rotfile` (every reopen after a rotation) |
| `datagram/` | the batched datagram send (#807): `sendmmsg(2)` on Linux, up to 64 datagrams a call over a connected socket, one `Write` each elsewhere — each payload still ONE datagram | `logger/writer/nettransport` (the udp seam), `logger/writer/journald` (`WriteBatch`) |

It sits beside its importers, at the deepest directory that holds them both —
the logger's, since one importer is a sink and the other a writer — so the
`internal/` rule says who may use it, as the observe family's own `internal/`
does for the OTLP emitter (ADR 0155 §1).

`datagram/`'s two importers are both writers; it sits here rather than in a
`writer/internal/` of its own so the logger keeps ONE private prefix, and the
`internal/` rule still admits nothing outside the logger.

## Do NOT

- Put Go code in this directory.
- Add a helper only one sink or writer uses: it belongs in that package.
