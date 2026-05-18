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

## Do NOT

- Add buffering inside a terminal sink — that is `async`'s job.
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
