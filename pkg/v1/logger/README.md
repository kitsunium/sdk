# `pkg/v1/logger`

**Layer**: public facade · **Code range**: 4100-4199

The stable v1 public API for SDK logging. Everything consumer code needs — `Logger`, `Attr`, `Level`, `Sink`, `Encoder`, `Builder` — is exposed here. Type signatures are frozen post-`v1.0.0`; breaking changes land in `pkg/v2`.

## Quick start (text on stderr)

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

## Quick start (custom sink topology)

`NewText` is sugar for the most common case (text encoder + console sink on a single `io.Writer`). Callers that need fan-out, async buffering, or custom transports use `NewWithSink` plus the `Sink` and `Encoder` ports:

```go
import (
    "context"
    "github.com/kitsunium/sdk/pkg/v1/logger"
)

func wire() (logger.Logger, error) {
    fan := logger.Multi(
        logger.ConsoleStderr(),                                  // text on stderr
        // file.New("/var/log/app.log") — see internal/service/logger/sink/file
        // syslog.New("udp", "127.0.0.1:514") — RFC5424 envelope
    )

    return logger.NewWithSink(logger.SinkConfig{
        Sink:     fan,                          // fan-out
        Encoder:  logger.TextEncoder(),         // optional; nil ⇒ text encoder
        MinLevel: logger.LevelInfo,
    })
}
```

## Zero-allocation hot path: the chainable `Builder`

`logger.Build(lg, lv).Str(...).Int(...).Send(ctx, msg)` runs through a sync.Pool-backed builder so the steady-state per-call cost is zero heap allocations once the pool is warm. Callers that already have a pre-built `[]Attr` use `LogAttrs` instead of `Logger.Log`'s variadic path:

```go
attrs := []logger.Attr{logger.String("svc", "api"), logger.Int("tries", 3)}
logger.LogAttrs(ctx, lg, logger.LevelInfo, "request", attrs)

// or chainable:
logger.Build(lg, logger.LevelInfo).
    Str("svc", "api").
    Int("tries", 3).
    Send(ctx, "request")
```

## Surface

### Types (aliases onto `internal/core/logger` and `internal/service/logger`)

```go
type Logger  = corelogger.Logger        // interface
type Attr    = corelogger.AttrValue     // key/value pair
type Sink    = corelogger.Sink          // transport port
type Encoder = encoder.Encoder          // format port
type Record  = corelogger.RecordEvent   // payload value
type Builder = svclogger.Builder        // chainable hot path
type Level   = level.Level              // int8
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

### Constructors

```go
type Config struct {
    Writer   io.Writer  // REQUIRED — nil returns WriterRequired
    MinLevel Level      // zero value = LevelInfo
}

type SinkConfig struct {
    Sink     Sink        // REQUIRED — nil returns SinkConfigRequired
    Encoder  Encoder     // optional; nil ⇒ TextEncoder()
    MinLevel Level       // zero value = LevelInfo
}

func NewText(cfg Config)         (Logger, error)  // text encoder + io.Writer
func NewWithSink(cfg SinkConfig) (Logger, error)  // arbitrary Sink + Encoder
func Default()                   (Logger, error)  // = NewText(Config{Writer: os.Stderr})
```

### Sink helpers

```go
func Multi(branches ...Sink) Sink    // fan-out across branches; per-sink errors joined
func ConsoleStderr()         Sink    // console.NewStderr()
func ConsoleStdout()         Sink    // console.NewStdout()
func TextEncoder()           Encoder // canonical text encoder bound to the system clock
```

For richer sinks (file, async, route, failover, sample, recover, syslog) consumers reach into `internal/service/logger/sink/<name>` directly today; v1 will surface re-exports as the contracts stabilise.

### Emission helpers

```go
func Debug(ctx, lg Logger, msg string, attrs ...Attr)
func Info(ctx, lg Logger, msg string, attrs ...Attr)
func Warn(ctx, lg Logger, msg string, attrs ...Attr)
func Error(ctx, lg Logger, msg string, attrs ...Attr)

// zero-alloc hot path
func LogAttrs(ctx, lg Logger, lv Level, msg string, attrs []Attr)
func Build(lg Logger, lv Level) Builder
func WithGroup(lg Logger, name string) Logger
```

### Attr constructors

```go
func String(key, val string)   Attr
func Int(key string, val int)  Attr
func Int64(key string, v int64) Attr
func Uint64(key string, v uint64) Attr
func Bool(key string, v bool)  Attr
func Float64(key string, v float64) Attr
func Duration(key string, v time.Duration) Attr
func Time(key string, v time.Time) Attr
func Any(key string, v any)    Attr
```

### Version

```go
var Version string                // injected via -ldflags
func FrameworkVersion() string    // Version, or "dev" if unset
```

## Error catalogue

| Code | Var                  | When it fires                                         |
|------|----------------------|-------------------------------------------------------|
| 4101 | `WriterRequired`     | `NewText(Config{Writer: nil})`                        |
| 4102 | `SinkConfigRequired` | `NewWithSink(SinkConfig{Sink: nil})`                  |

Introspect via `pkg/v1/errs`:

```go
_, err := logger.NewWithSink(logger.SinkConfig{})
if errs.HasCode(err, 4102) { /* misconfigured */ }
```

## Breaking change vs the pre-errors API

`NewText(Config{})` used to silently default `Writer` to `os.Stderr`. Since the errors MR it returns `(nil, WriterRequired)` — explicit construction prevents logs being silently redirected. `NewWithSink` follows the same discipline with `SinkConfigRequired`. Callers that want the one-liner use `Default()`.

## Framework version

`Version` is empty in local dev; `FrameworkVersion()` returns `"dev"` in that case. Inject at build time:

```bash
go build -ldflags "-X github.com/kitsunium/sdk/pkg/v1/logger.Version=v0.1.0" ./...
```

Every record emitted by a logger built via `NewText` / `NewWithSink` / `Default` carries `framework_version="<value>"` automatically.

## Do NOT

- Import `github.com/kitsunium/sdk/internal/*` — Go's internal rule blocks it anyway, but API-wise stay on `pkg/v1/*` for long-term stability.
- Forge errors by calling `errs.Define` from a consumer package. Introspect errors via `pkg/v1/errs` instead.
- Use the `Builder` after `Send` — it returns to the recycler and the next caller will reuse the same struct.
- Set `Version` at runtime from your application code. Use the `ldflags` injection.

## Tests

- `logger_external_test.go` — `NewText` happy path, `WriterRequired` rejection, framework_version attr injection, level helpers, Attr constructors.
- `sink_external_test.go` — `NewWithSink` happy path + `SinkConfigRequired` rejection, `Multi` fan-out, `Build` chain, `LogAttrs`, `WithGroup`, console / encoder helpers.
- `version_external_test.go` — `FrameworkVersion()` never returns empty.

Coverage: ≥ 90 % across the facade.
