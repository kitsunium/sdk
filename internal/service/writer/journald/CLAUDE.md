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

## Cost — deliberately not benchmarked, and what was found instead

**A `BENCH.md` here would report the cost of `write(2)` on an `AF_UNIX`
datagram socket — a kernel property, not a property of this package.** The whole
per-record path is: strip at most one trailing newline, append the 8 constant
bytes of `MESSAGE=`, append the payload, append one byte, one `conn.Write`. No
per-field loop, no length-prefixed framing, no rune decode, no formatting, no
timestamp, no variable-size `make`. Two `memmove`s separate the encoder from the
kernel, one of them inherited from `async`. There is nothing here whose cost is
this package's own.

A second reason: five of the six test functions hand you `net.Pipe()`, which is
an unbuffered in-memory **stream** with no file descriptor and no syscall. A
benchmark written on that harness measures a goroutine rendezvous and two
context switches — micro-seconds — in place of a write into a socket buffer.
`journald_external_test.go` is the only harness that opens a real `unixgram`
socket, and it does so once, at one 14-byte payload.

A third: nothing reachable can call this. `pkg/v1/logger/writer` blank-imports
`console`, `file` and `rotfile` only, no in-tree file imports this package
outside its own tests and the `errs` ownership table, and Go's `internal/`
firewall stops a consumer of the published `pkg` module importing it directly.
The writer is registered by nobody.

### Four findings the reconnaissance produced instead of numbers

These are recorded rather than fixed, because none of them is a performance
question and two of them change what reaches the journal.

1. **The record's level is discarded.** `Write` takes a `corelogger.RecordEvent`
   and never reads it — confirmed by `-gcflags=-m` reporting `r does not
   escape`. The frame carries `MESSAGE=` and nothing else, so there is no
   `PRIORITY=`, no `SYSLOG_IDENTIFIER` and no `_PID`: every SDK record lands at
   journald's default priority and `journalctl -p err` returns nothing. Adding
   `PRIORITY=` is the right functional fix and it creates the per-field branch
   that does not exist today, so it must be measured before AND after.

2. **`buf` has no capacity ceiling, and the layer directly above it has one.**
   `async`'s drainer releases a per-entry backing array above `maxSaneCap`
   (64 KiB) citing CWE-400 / CWE-789, so async decides a 200 KB payload is too
   dangerous to retain — then hands it downstream, where this sink keeps a copy
   for the process's lifetime. Two adjacent layers disagree about the same
   hazard.

3. **An oversized record is `EMSGSIZE` with no fallback and no test.** A
   connected `SOCK_DGRAM` refuses a datagram above the socket's limit; this sink
   performs no size check and implements neither the memfd nor the `SCM_RIGHTS`
   path the native protocol defines, so such a record becomes a hard
   `JournaldWriteFailed`. No test exercises any payload size on either harness.

4. **"Zero allocations" is claimed in three places and asserted nowhere.** The
   package `CLAUDE.md`, the file comment and the `buf` field comment all state
   it; `grep` for `AllocsPerRun` or `Benchmark` across the package returns
   nothing, and no `//go:build !race` file exists, so the claim is also absent
   from `tools/alloc-lane-targets.txt`. The claim is structurally right — once
   `cap(buf)` covers the record the appends allocate nothing — but "one-time
   growth" holds only in the depth dimension; in the width dimension it is
   finding 2.

### One security property asserted by a comment and enforced by no test

The frame is correct **only because** the SDK's own encoders strip CR, LF and
NUL — which the in-tree text encoder does, in `encoder.AppendSanitized`. But
`Encoder` is a public port, this package neither imports nor tests against any
encoder, and the sink validates nothing. A custom encoder that emits a raw
newline turns `MESSAGE=x\nPRIORITY=0\n` into a journal entry carrying a
priority the caller did not choose: journal field injection. The exposure is
currently bounded by the third reason above — nothing reachable registers this
writer — which is why it is written down here rather than treated as an
incident.

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
