# `pkg/v1/logger`

**Layer**: public facade · **Code range**: 4100-4199

The stable v1 public API for SDK logging. Everything consumer code needs — `Logger`, `Attr`, `Level` — is exposed here. Type signatures are frozen post-`v1.0.0`; breaking changes land in `pkg/v2`.

## Quick start

```go
import (
    "context"
    "os"

    "github.com/kitsunium/sdk/pkg/v1/logger"
)

func main() {
    lg, err := logger.NewText(logger.Config{
        Writer:   os.Stderr,
        MinLevel: logger.LevelInfo,
    })
    if err != nil {
        panic(err)
    }

    ctx := context.Background()
    logger.Info(ctx, lg, "service started",
        logger.String("env", "prod"),
        logger.Int("port", 8080))
}
```

Emits:

```
2026-04-19T12:34:56.789Z INFO service started env="prod" port=8080 framework_version="dev"
```

## Surface

### Types (aliases onto `internal/core/logger`)

```go
type Logger  = corelogger.Logger       // interface
type Attr    = corelogger.AttrValue    // key/value pair
type Level   = level.Level             // int8
```

### Levels (aliases onto `internal/core/logger/level`)

```go
const (
    LevelDebug Level = level.Debug
    LevelInfo  Level = level.Info
    LevelWarn  Level = level.Warn
    LevelError Level = level.Error
)
```

### Config + constructors

```go
type Config struct {
    Writer   io.Writer  // REQUIRED — nil returns WriterRequired
    MinLevel Level      // zero value = LevelInfo
}

func NewText(cfg Config) (Logger, error)  // main entry point
func Default()           (Logger, error)  // = NewText(Config{Writer: os.Stderr})
```

### Emission helpers

```go
func Debug(ctx context.Context, lg Logger, msg string, attrs ...Attr)
func Info(ctx context.Context, lg Logger, msg string, attrs ...Attr)
func Warn(ctx context.Context, lg Logger, msg string, attrs ...Attr)
func Error(ctx context.Context, lg Logger, msg string, attrs ...Attr)
```

### Attr constructors

```go
func String(key, val string) Attr
func Int(key string, val int)  Attr
```

### Version

```go
var Version string                // injected via -ldflags
func FrameworkVersion() string    // Version, or "dev" if unset
```

## Error catalogue

| Code | Var              | When it fires                      |
|------|------------------|------------------------------------|
| 4101 | `WriterRequired` | `NewText(Config{Writer: nil})`     |

Introspect via `pkg/v1/errs`:

```go
_, err := logger.NewText(logger.Config{})
if errs.HasCode(err, 4101) { /* misconfigured */ }
```

## Breaking change vs the pre-errors API

`NewText(Config{})` used to silently default `Writer` to `os.Stderr`. Since this MR it returns `(nil, WriterRequired)` — explicit construction prevents logs being silently redirected. Callers that want the one-liner use `Default()`.

## Framework version

`Version` is empty in local dev; `FrameworkVersion()` returns `"dev"` in that case. Inject at build time:

```bash
go build -ldflags "-X github.com/kitsunium/sdk/pkg/v1/logger.Version=v0.1.0" ./...
```

Every record emitted by a logger built via `NewText` / `Default` carries `framework_version="<value>"` automatically.

## Do NOT

- Import `github.com/kitsunium/sdk/internal/*` — Go's internal rule blocks it anyway, but API-wise stay on `pkg/v1/*` for long-term stability.
- Forge errors by calling `errs.Define` from a consumer package. Introspect errors via `pkg/v1/errs` instead.
- Set `Version` at runtime from your application code. Use the `ldflags` injection.

## Tests

- `logger_external_test.go` — NewText happy path, WriterRequired rejection, framework_version attr injection, level helpers, Attr constructors.
- `version_external_test.go` — `FrameworkVersion()` never returns empty.

Coverage: 84.2%.
