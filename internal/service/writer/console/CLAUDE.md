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
| `console.go` | `Writer` singleton, `consoleFactory` (`Name` / `Open`), `pickStream` |

No `codes.go` — a wrong-type `Config` returns the shared
`core/writer.WriterConfigInvalid`; the underlying sink owns its own I/O codes
(`0.3.13.*`).

## Behaviour

- `Open` type-asserts `writer.ConsoleConfig`; a mismatch returns
  `WriterConfigInvalid`.
- `Stream` selects `console.NewStderr()` (explicit) or `console.NewStdout()`
  (default / zero value).
- The result is wrapped in `levelgate.New(base, cfg.MinLevel)` — a no-op when
  `MinLevel == Info` (inherit).

## Do NOT

- Add console I/O here — that belongs to `service/logger/sink/console`. This
  package only resolves config → sink.
- Call `Close` on the result expecting it to close `os.Stdout/Stderr`; the
  underlying console sink owns that contract (it is a no-op there).

## Verification

```
bazel test --config=race //internal/service/writer/console:console_test
# Fallback
cd internal/service && GOWORK=off go test -race -cover ./writer/console/...
```
