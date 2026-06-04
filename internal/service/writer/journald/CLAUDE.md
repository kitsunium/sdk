# internal/service/writer/journald/

## Purpose

Registers the **"journald"** writer factory (ADR 0015): a stdlib unix-datagram
sink that ships records to the systemd journal. Importing the package
self-registers the factory (no `init()`), so `writer.Open("journald",
journald.Config{…})` and YAML `FromConfig` topologies resolve. Linux-only in
practice (the socket is systemd's), but the code is plain stdlib `net` and builds
everywhere; on a host without journald the `Open` simply fails with
`JournaldOpenFailed`.

## Contents

| File | Role |
|---|---|
| `journald.go` | `Writer` singleton, `journaldFactory` (`Name`/`Open`) — connects the socket + composes the chain |
| `journald_config.go` | `Config` value type (socket path, dialer seam, level, ring) |
| `journald_sink.go` | `journaldSink` (`Write`/`Flush`/`Close`) — the datagram framing terminal |
| `decode.go` | `journaldFactory.Decode` (`core/writer.Decoder`) + key coercion helpers |
| `codes.go`, `errors.go` | sentinels — range 0.3.31.\* (service slot 0x1f) |

## Behaviour

- `Open` connects `unixgram` to the socket (default `/run/systemd/journal/socket`,
  overridable via `Config.SocketPath`) through `Config.Dialer` (or `net.Dial`),
  then composes `levelgate(async(journaldSink))` — the level floor drops
  below-floor records before the non-blocking ring; the producer never blocks on
  the socket (drops surface via `OnDrop`).
- `Write` frames each record as a single `MESSAGE=<line>\n` journal entry into a
  **reused buffer** and sends it as one datagram, so the steady-state Write path
  is **0 alloc** (the buffer grows once, then is reused under the mutex). Framing
  note: the native protocol length-prefixes multiline values; this sink emits the
  simple single-line form, valid because the SDK encoders strip CR/LF/NUL.

## Decoder (YAML-reachable via `FromConfig`)

| Key | Type | Maps to |
|---|---|---|
| `socket_path` | string | `SocketPath` (default systemd socket when absent) |
| `min_level` | string | `MinLevel` via `level.ParseLevel` |
| `buffer_size` | int | `BufferSize` (async ring capacity) |

The `Dialer` seam is code-only. A malformed shape returns the shared
`core/writer.WriterConfigInvalid` (no per-package code), tagged with the writer
name only — never the value (secret gate).

## Error catalogue — range 0.3.31.\*

| Code | Sentinel | Trigger |
|---|---|---|
| 0.3.31.1 | `JournaldOpenFailed` | the unix-datagram socket could not be connected (EX_IOERR) |
| 0.3.31.2 | `JournaldWriteFailed` | a datagram send / close failed, or a cancelled ctx (EX_IOERR) |

## Do NOT

- Emit multiline messages without length-prefix framing — this sink assumes the
  encoder already produced a single line.
- Echo the socket path or record content into an error.
- Buffer inside `journaldSink` — that is `async`'s job.

## Verification

```sh
bazel test --config=race //internal/service/writer/journald:journald_test
# Fallback
cd internal/service && GOWORK=off go test -race -cover ./writer/journald/...
```
