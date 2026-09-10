# internal/service/writer/console/

## Purpose

Registers the **"console"** writer factory (ADR 0012). Importing this package
self-registers the factory (`var Writer = writer.Register(&consoleFactory{})`,
no `init()`), so `writer.Open("console", logger.ConsoleConfig{…})` resolves.
The factory is a thin adapter: it delegates to the existing terminal sink in
`service/logger/sink/console` and wraps it with the optional per-writer
`MinLevel` via `service/writer/levelgate`.

## Contents

| File | Role |
|---|---|
| `console.go` | `Writer` singleton, `consoleFactory` (`Name` / `Open` / `Decode`), `pickStream` + decode helpers |

No `codes.go` — a wrong-type `Config` returns the shared
`core/writer.WriterConfigInvalid`; the underlying sink owns its own I/O codes
(`0.3.13.*`).

## Behaviour

- `Open` type-asserts `writer.ConsoleConfig`; a mismatch returns
  `WriterConfigInvalid`.
- `Stream` selects `console.NewStdout()` (explicit) or `console.NewStderr()`
  (default / zero value). ADR 0030: stdout is a protocol channel for many
  processes, so it is never where an unspecified config lands.
- The result is wrapped in `levelgate.New(base, cfg.MinLevel)` — a no-op when
  `MinLevel == Info` (inherit).

### Decoder (YAML-reachable via `FromConfig`)

`consoleFactory` also implements `core/writer.Decoder`, so the default-active
console writer is reachable from a topology config blob. Recognised option keys
(under a writer entry's `config:` map):

| Key | Type | Default | Maps to |
|---|---|---|---|
| `target` | string | `stderr` | `Stream`: `"stderr"`/`""` → stderr, `"stdout"` → stdout |
| `min_level` | string | inherit (info) | `MinLevel` via `level.ParseLevel` (`debug`/`info`/`warn`/`error`) |

Unknown `target`, unknown `min_level`, or a non-string value for either key
returns the shared `core/writer.WriterConfigInvalid` (no new code). **Secret
gate:** the error names only the writer (`writer=console` field), never the
offending value.

## Cost

Measured in `BENCH.md` (median of five runs, AMD EPYC 7351P). A console writer
writes to a terminal in production and to something else in every test, so every
row names its destination. **No row is a terminal** — a terminal's cost is the
terminal emulator's and is not reproducible.

| one record, 83-byte line | ns/op | allocs | who has this destination |
|---|---:|---:|---|
| `io.Discard` | 26.63 | 0 | nobody — the package floor |
| `/dev/null` | 302.20 | 0 | `app 2>/dev/null` |
| **pipe with a live consumer** | **1 868** | 0 | a container's stderr — **believe this one** |
| `writer.Open("console", ConsoleConfig{})` | 73.43 | 2 (26 B) | once, at start-up |
| the same with `MinLevel: Error` | 100.50 | 3 (50 B) | +24 B is the `levelgate` wrapper |

- **The writer adds ZERO allocations to an emit.** A full emit through the text
  encoder and `genericHandler` costs 1 allocation / 128 B with no transport and
  1 allocation / 128 B through this writer; the allocation profile puts 98.91 %
  of the objects on the handler's attrs clone and none on the transport. This is
  the console half of the answer to "what does a WRITER add to the SDK's one
  alloc per emit". `TestConsoleWriterAddsNoAllocationToAnEmit` pins the DELTA
  (not a constant — the clone is not this package's property), is
  mutation-checked in its own doc comment, and is covered by the race-off alloc
  lane (`tools/alloc-lane-targets.txt`, SDK-wide rule 12).
- **This SDK's code is 1.94 % of an emit.** `consoleSink.Write` is 21.79 % of
  the CPU profile, of which `internal/poll.(*FD).Write` — the kernel — is
  19.85 %.
- **74 % of the package floor is one mutex**: an uncontended `sync.Mutex`
  Lock/Unlock pair costs 19.63 ns of the `io.Discard` row's 26.63 ns on this Zen
  1 part, and work placed inside that pair is nearly free (an interface write
  that costs 2.84 ns alone adds 0.34 ns there). That is also why `BENCH.md` §2
  **refuses to rank the four `context` shapes** it measured: the guard being
  timed costs less than 0.4 ns and the rows span 2.9 ns.
- **`/dev/null` does not scale with the payload** (1.7 % across 96 B → 8 KiB); a
  **pipe does** (0.41 ns/byte). A service emitting large structured lines to a
  log collector pays per byte.
- **Eight goroutines get the throughput of one.** A `Sink` is a serialisation
  point — that is what makes a log line atomic — and at eight-way contention the
  `io.Discard` row costs 3.24× its uncontended self.

## Do NOT

- Add console I/O here — that belongs to `service/logger/sink/console`. This
  package only resolves config → sink.
- Call `Close` on the result expecting it to close `os.Stdout/Stderr`; the
  underlying console sink owns that contract (it is a no-op there).
- Add a per-record decorator in `Open`. Today the factory contributes no
  per-record code at all, which is exactly why the writer adds no allocation to
  an emit; a decorator that touches the bytes is the mutation
  `TestConsoleWriterAddsNoAllocationToAnEmit` exists to catch, and it doubles the
  logger's advertised allocation budget.

## Verification

```sh
bazel test --config=race //internal/service/writer/console:console_test
# Fallback
cd internal/service && GOWORK=off go test -race -cover ./writer/console/...
```
