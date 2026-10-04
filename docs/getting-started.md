# Getting started

This page walks you from "I have a fresh Go project" to running code that uses **logger**, **codec**, and **errs**. Five minutes, no toolchain assumptions beyond a working Go install.

## Requirements

- Go **1.27.1 or newer — mandatory**. The SDK module declares `go 1.27.1` and `MODULE.bazel` pins the toolchain to 1.27.1. An older `go` command (1.21 or later) with the default `GOTOOLCHAIN=auto` downloads and runs Go 1.27.1 itself; with `GOTOOLCHAIN=local`, or where it cannot download, it refuses the build — the `go` directive is the requirement, not any single language feature. (The features the SDK does use — `for b.Loop()`, `b.Context()`, range-over-int — landed in 1.22/1.24 and are not what sets the floor.)
- A Go module to import from: `go mod init github.com/<you>/<project>` if you don't have one yet

## Install

The SDK is one Go module, `github.com/kitsunium/sdk` — `pkg/v1`, the framework and the internals they are built on — and it links the standard library and nothing else. Require the module once; only the packages you import end up in your binary:

```bash
go get github.com/kitsunium/sdk@latest
```

Writing the imports below first and running `go mod tidy` works as well. Up to v0.17.0 the public packages were the modules `github.com/kitsunium/sdk/pkg` and `…/framework`, and Go resolves an import nothing in your go.mod provides to the module with the LONGEST path whose latest version provides it; v0.18.0 gave both a last version that holds no package, so the tidy resolves the imports to `github.com/kitsunium/sdk`. A go.mod that already requires `…/pkg` migrates with the one command in the root `README.md` (ADR 0162).

## Hello, codec

`codec.Marshal` dispatches over a `Format` registry — text, binary, base-N, all the same call:

```go
package main

import (
    "fmt"

    "github.com/kitsunium/sdk/pkg/v1/data/codec"
)

func main() {
    payload := map[string]any{"hello": "world", "answer": 42}

    // JSON
    out, err := codec.Marshal(codec.JSON, payload)
    if err != nil {
        panic(err)
    }
    fmt.Println(string(out))

    // Switch the wire format with one string — same payload, same call.
    cbor, _ := codec.Marshal(codec.CBOR, payload)
    fmt.Printf("CBOR is %d bytes\n", len(cbor))
}
```

Twenty-four Format names are registered out of the box (`asn1-der`, `bson`, `cbor`, `csv`, `flatbuffers`, `form`, `json`, `msgpack`, `multipart`, `ndjson`, `pem`, `tlv`, `toml`, `xml`, `yaml`, and the nine base-N variants `base16`/`base32`/`base45`/`base58`/`base62`/`base64`/`base64url`/`hex`/`ascii85`). `codec.Available()` returns the live list if you would rather ask the registry than trust this page. See the [codec page](../data/codec/) for the full surface.

## Hello, logger

The structured logger writes to any sink you point it at. The simplest call is the package-level `Info`/`Warn`/`Error`/`Debug` set:

```go
package main

import (
    "context"

    "github.com/kitsunium/sdk/pkg/v1/observe/logger"
)

func main() {
    lg, err := logger.Default()
    if err != nil {
        panic(err)
    }

    ctx := context.Background()
    logger.Info(ctx, lg, "service started",
        logger.String("env", "prod"),
        logger.Int("port", 8080),
    )
}
```

For a more readable hot path use the chainable builder (same 1 alloc/emit as the variadic form — it buys ergonomics, not allocations):

```go
logger.Build(lg, logger.LevelInfo).
    Str("env", "prod").
    Int("port", 8080).
    Send(ctx, "service started")
```

See the [logger page](../observe/logger/) for sink composition (multi / async / route / failover / sample / recover / tee / encwrite) and the perf rows that back the 1-alloc-per-emit figure.

## Hello, errs

The SDK emits typed errors with dotted-quad codes. Consumer code introspects with the `Of`-accessors:

```go
package main

import (
    "fmt"

    "github.com/kitsunium/sdk/pkg/v1/data/codec"
    "github.com/kitsunium/sdk/pkg/v1/errs"
)

func main() {
    // Trigger a known failure path.
    _, err := codec.Marshal(codec.Format("not-a-real-format"), nil)
    if err == nil {
        return
    }

    if code, ok := errs.CodeOf(err); ok {
        fmt.Printf("code=%s reason=%s\n", code, errs.ReasonOf(err))
        fmt.Printf("safe to surface: %q\n", errs.PublicOf(err))
        // PrivateOf(err) is diagnostic-only — never put it in a user-facing channel.
    }
}
```

The `errs` package never lets consumer code FORGE an SDK error — only introspect one. See the [errs page](../errs/) for the full accessor list (`HTTPStatusOf`, `ExitCodeOf`, `HasCode`, `NewPrefixMatcher`, …).

## Putting it together

A typical entry point wires the three packages into a single boot sequence:

```go
package main

import (
    "context"
    "os"

    "github.com/kitsunium/sdk/pkg/v1/data/codec"
    "github.com/kitsunium/sdk/pkg/v1/errs"
    "github.com/kitsunium/sdk/pkg/v1/observe/logger"
)

func main() {
    lg, err := logger.Default()
    if err != nil {
        // Bootstrap failures pre-logger: write the typed code to stderr.
        if code, ok := errs.CodeOf(err); ok {
            _, _ = os.Stderr.WriteString(fmt.Sprintf("boot failed: %s\n", code))
        }
        os.Exit(errs.ExitCodeOf(err))
    }

    ctx := context.Background()
    raw, err := codec.Marshal(codec.JSON, map[string]any{"booted": true})
    if err != nil {
        logger.Error(ctx, lg, "marshal failed",
            logger.String("code", fmt.Sprintf("%v", errs.CodeOf(err))),
        )
        os.Exit(errs.ExitCodeOf(err))
    }
    _ = raw
}
```

## Where to go next

- [Concepts](../concepts/) — the four-layer model (kernel → core → service → pkg/v1), the error code allocation table, codec dispatch semantics
- [codec](../data/codec/), [errs](../errs/), [logger](../observe/logger/) — full Go doc surface per package, with the inline benchmarks at the bottom of each page
- [Changelog](../changelog/) — what changed across releases
- [For contributors ↗](../contributors/) — ADRs, verification gates, benchmark methodology

If you hit something that doesn't behave as documented, the `errs` introspection accessors are the fastest way to attach the right context to a bug report: dump `CodeOf(err)`, `ReasonOf(err)`, and the value of `PublicOf(err)` so we can locate the failure path without guesswork.
