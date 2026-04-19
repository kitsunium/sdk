# `internal/kernel/level`

**Layer**: kernel · **Code range**: 1100-1199 (reserved, no emissions today)

Severity levels for log records. Stdlib-only. Signatures mirror `log/slog` for ergonomic familiarity while staying inside the kernel's no-external-deps rule.

## Surface

```go
type Level int8

const (
    Debug Level = -4
    Info  Level = 0
    Warn  Level = 4
    Error Level = 8
)

func (l Level) String() (label string)  // "DEBUG" | "INFO" | "WARN" | "ERROR"
```

## Contract

- `Level` is an `int8`; arbitrary values are legal (e.g., `3` falls in the Info window, `16` maps to `ERROR`). Threshold windows are `[Debug, Info) / [Info, Warn) / [Warn, Error) / [Error, +∞)`.
- `String()` is the only method; no Parse/format helpers on purpose. Handlers that need localisation build it above this package.
- Constants are spaced (`-4 / 0 / 4 / 8`) so downstream levels can be interpolated without renumbering.

## Typical use

```go
import "github.com/kitsunium/sdk/internal/kernel/level"

if record.Level >= level.Warn {
    // emit
}
```

## Do NOT

- Add a `Parse(string) (Level, error)` helper in the kernel — keep parsing at the service or public-facing layer.
- Import `log/slog` here — `level` is the one package this SDK does not want coupled to the stdlib slog API.

## Tests

`level_external_test.go` covers every String window plus the ordering invariant `Debug < Info < Warn < Error`.
