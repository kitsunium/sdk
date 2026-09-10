<!-- updated: 2026-05-18T14:30:00Z -->
# internal/service/logger/sink/

## Purpose

Terminal `core/logger.Sink` implementations — the bottom of every sink
chain. Each sub-package owns one transport and one PP slot in the dotted-
quad error registry (ADR 0006).

## Contents

| Package    | Transport                          | Code prefix |
|---|---|---|
| `console/` | `io.Writer` (stderr / stdout / custom) under a mutex | 0.3.13.\* |
| `file/`    | `*os.File` opened append-only, symlink-rejecting     | 0.3.14.\* |
| `syslog/`  | `net.Conn` over UDP or TCP, RFC5424 minimal envelope | 0.3.15.\* |

## Conventions

- **Constructors return the `Sink` interface**, never the concrete type
  (IFACE-PLUGIN). The unexported struct stays in its own package.
- **Concurrency.** Every sink implements the "safe for concurrent use"
  Sink contract — typically via a `sync.Mutex` around the underlying
  writer (`console`, `file`, `syslog`). UDP datagrams under PIPE_BUF are
  already atomic but the mutex is kept uniform.
- **Context cancellation.** Every `Write` / `Flush` checks `ctx` first and
  returns a wrapped `CtxCancelled` sentinel; the cause remains
  `errors.Is(ctx.Err())` matchable.
- **Wrapping I/O errors.** Underlying `Write` / `Sync` / `Close` failures
  flow through `errs.Wrap` so `errors.Is` against the original cause
  still matches; the wrapping sentinel sets `ExitCode 74` (EX_IOERR).

## Cost

Benchmarked against one shared discard control in one run — the report is
`internal/service/logger/BENCH.md` §2. Per record, on an 8-core EPYC 7351P:

| sink | ns/op | allocs | what dominates |
|---|---:|---:|---|
| `console` → `io.Discard` | 21.4 | 0 | the mutex and the ctx check, +14 ns over a bare call |
| `syslog` — framing only | 143 | 1 (192 B) | the RFC5424 frame buffer |
| `memory` | 274 | 1 (487 B) | the defensive deep clone of `Attrs` |
| `file` → real file | 1 060 | 0 | **`write(2)`, 98 % of it** |
| `syslog` → UDP loopback | 5 322 | 2–3 | **the datagram, 97 % of it** |

Nothing in this subtree is worth optimising, and the numbers are why: the SDK's
own contribution to a `file` write is 2 % of its cost, `console` is a mutex
that the atomic-line contract requires, `memory`'s allocation IS the sink's
purpose (it retains the record, not the bytes), and `syslog`'s frame buffer is
2.7 % of a real send. `memory` is also **unbounded** — it retains every record
until `Reset`, which is fine for a test and fatal for a long-running process.

**`syslog` cannot reach a local syslog daemon.** `NewWithConfig` accepts only
`udp` and `tcp`, so the `/dev/log` unix datagram socket that every Linux box
carries is not a destination this sink has. That is a capability gap, not an
environment limitation, and it is why the report carries no `/dev/log` number.

## Do NOT

- Add buffering inside a terminal sink — that is `async`'s job. The `file`
  sink's 1 060 ns is one syscall per record and buffering is the only lever
  that would move it; the lever belongs to `async`, deliberately.
- Close `os.Stdout` / `os.Stderr` from `console.Close`; the caller owns
  those file descriptors.
- Reach into the encoder's bytes; sinks treat the payload as opaque.

## Verification

```
bazel test --config=race //internal/service/logger/sink/...
```

## Subtree

- `console/` — stderr/stdout/custom `io.Writer`
- `file/`    — append-only on-disk file, symlink hardened
- `syslog/`  — minimal RFC5424 UDP/TCP
