# kitsunium/sdk

A Go SDK providing a normed, performant toolbox for downstream applications: structured logging, universal codec dispatch, and typed errors with stable wire codes.

## Packages

| Package | What it does |
|---|---|
| [`pkg/v1/logger`](./pkg/v1/logger) | Zero-allocation structured logger. Multi-sink (console / file / syslog), middleware chain (multi, async, route, failover, sample, recover), build-time version stamping. |
| [`pkg/v1/codec`](./pkg/v1/codec) | Universal codec dispatch over a `Format` registry. 18 formats covered by a single `Marshal` / `Unmarshal` / `NewEncoder` / `NewDecoder` API — `asn1-der`, `cbor`, `csv`, `flatbuffers`, `json`, `msgpack`, `ndjson`, `pem`, `tlv`, `toml`, `xml`, `yaml` + 6 base-N encodings. |
| [`pkg/v1/errs`](./pkg/v1/errs) | Typed errors with dotted-quad codes (`MM.LL.PP.SS`) + Public/Private split. Read-only introspection: `CodeOf`, `ReasonOf`, `HasCode`, `NewPrefixMatcher`. |

## Install

```bash
go get github.com/kitsunium/sdk/pkg/v1/codec
go get github.com/kitsunium/sdk/pkg/v1/errs
go get github.com/kitsunium/sdk/pkg/v1/logger
```

## Quick example

```go
package main

import (
    "fmt"
    "github.com/kitsunium/sdk/pkg/v1/codec"
)

func main() {
    payload := map[string]any{"hello": "world"}
    out, err := codec.Marshal(codec.JSON, payload)
    if err != nil {
        panic(err)
    }
    fmt.Println(string(out))
}
```

## What makes this SDK different

- **Layered architecture** — `kernel` (stdlib-only primitives) → `core` (interfaces + values) → `service` (implementations) → `pkg/v1` (stable public alias layer). Dependency direction enforced by Bazel `visibility` rules.
- **Typed errors throughout** — `fmt.Errorf` / `errors.New` are banned in production code. Every error carries a wire-safe `Public` message and a log-only `Private` envelope. AST audit gates the build.
- **One Marshal/Unmarshal for everything** — text, binary, base-encoded — `codec.Marshal(format, v)` works the same way regardless of the underlying wire shape.
- **Multi-module workspace** — `internal/kernel`, `internal/core`, `internal/service`, `pkg/v1`, root. Each can be built standalone with `GOWORK=off`; `go.work` is the umbrella.

## Releases

Versioning follows [semver](https://semver.org/) and Go's sub-directory tag convention: `pkg/<major>/v<MAJOR>.<MINOR>.<PATCH>`. Each major version (`pkg/v1`, future `pkg/v2`) has its own independent release cadence. Full release workflow lives in ADR 0007 (`docs/adr/`).

## Documentation

Full versioned documentation lives at the docs portal (run `make serve` from the repo root, or browse the published deployment). Highlights:

- **Concepts** — the 4-layer model, error semantics, codec dispatch
- **Changelog** — auto-generated from git log per release
- **Per-package reference** — `codec`, `errs`, `logger` with their full Go doc surfaces
- **ADRs** — architecture decisions, reachable from the "For contributors ↗" footer link

## Repository layout

```
internal/        stdlib-only primitives (kernel) + domain interfaces (core) + concrete impls (service)
pkg/v1/          stable public API — type aliases + ergonomic helpers
docs/            ADRs + the Astro-based docs site
scripts/release/ release tooling — see ADR 0007
tools/           build-time helpers (workspace_status, genindex)
```

## Verification

```bash
make build   # bazel mod tidy + gazelle + gofumpt + bazel build //...
make test    # every *_test target green incl. AST audits
make lint    # drift check (read-only): mod tidy + gazelle + gofumpt + ktn-linter
make bench   # regenerate codec BENCH.md from real Go benchmarks
```

## License

MIT — see [LICENSE](./LICENSE) for the full text.
