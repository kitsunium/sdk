# kitsunium/sdk

A Go SDK providing a normed, performant toolbox for downstream applications: structured logging, universal codec dispatch, typed errors with stable wire codes, a crypto suite, and OS process supervision.

## Packages

| Package | What it does |
|---|---|
| [`pkg/v1/logger`](./pkg/v1/logger) | Structured logger with a one-allocation-per-emit hot path (see its BENCH.md — the `sync.Pool` recycles the builder, the handler still clones the attrs). Multi-sink (console / file / syslog / memory), middleware chain (multi, async, route, failover, sample, recover, tee, encwrite), build-time version stamping. |
| [`pkg/v1/codec`](./pkg/v1/codec) | Universal codec dispatch over a `Format` registry. 23 formats covered by a single `Marshal` / `Unmarshal` / `NewEncoder` / `NewDecoder` API — `asn1-der`, `bson`, `cbor`, `csv`, `flatbuffers`, `form`, `json`, `msgpack`, `ndjson`, `pem`, `tlv`, `toml`, `xml`, `yaml` + 9 base-N encodings (`base16`, `base32`, `base45`, `base58`, `base62`, `base64`, `base64url`, `hex`, `ascii85`). |
| [`pkg/v1/errs`](./pkg/v1/errs) | Typed errors with dotted-quad codes (`MM.LL.PP.SS`) + Public/Private split. Construction (`New`, `Wrap`, `Field`) and read-only introspection: `CodeOf`, `ReasonOf`, `HasCode`, `NewPrefixMatcher`. |
| [`pkg/v1/crypto`](./pkg/v1/crypto) + [`hash`](./pkg/v1/hash), [`sign`](./pkg/v1/sign), [`mac`](./pkg/v1/mac), [`kdf`](./pkg/v1/kdf), [`agree`](./pkg/v1/agree), [`password`](./pkg/v1/password) | AEAD seal/open with hidden nonces, hashing, signatures, MACs, key derivation, key agreement and password hashing behind scheme registries. |
| [`pkg/v1/proc`](./pkg/v1/proc) + [`process`](./pkg/v1/process), [`signal`](./pkg/v1/signal), [`reaper`](./pkg/v1/reaper), [`rlimit`](./pkg/v1/rlimit), [`cgroup`](./pkg/v1/cgroup), [`sdnotify`](./pkg/v1/sdnotify), [`sdlisten`](./pkg/v1/sdlisten) | OS process supervision: spawn, signals, subreaping, resource limits, cgroups and systemd integration. Uniform typed `UnsupportedPlatform` where a kernel offers no native mechanism (ADR 0018). |

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
- **The rules are checkable from outside** — the conventions this SDK states in its ADRs are enforced on *your* codebase too, by a tool the SDK ships:
  ```sh
  go run github.com/kitsunium/sdk/tools/sdkguard@latest -level=invariant ./...
  ```
  It catches the defects that never announce themselves — a second logging pipeline beside the SDK logger (two thresholds, two formats, half-stamped records), stdout used as a log destination under a stdio protocol, a `logger.Version` assigned at runtime. Rules target *constructs*, never imports, so `*slog.Logger` fields and `fmt.Fprintln(os.Stdout, …)` stay legitimate. It also warns — without failing your build — when your `go.mod` pins an SDK older than the latest release, which matters here because a patch is cut whenever an internal package changes, carrying fixes no public-API changelog would show. See ADR 0033 and `tools/sdkguard/CLAUDE.md`.
- **Multi-module workspace** — `internal/kernel`, `internal/core`, `internal/service`, `pkg`, root. Each can be built standalone with `GOWORK=off`; `go.work` is the umbrella over exactly those five. `e2e/`, `tools/genindex/` and `tools/sdkguard/` are auxiliary modules held deliberately outside it (adding them breaks Bazel's `go_deps`, which reads `go.work`) — build those with `GOWORK=off`.

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
tools/           build-time helpers (workspace_status, genindex, alloc-lane-targets)
                 + sdkguard/ — consumer-facing rule enforcement (ADR 0033)
e2e/             real-kernel conformance harness (auxiliary module, GOWORK=off)
```

## Verification

```bash
make build       # bazel mod tidy + gazelle + gofumpt + bazel build //...
make test        # every *_test target green incl. AST audits
make test-alloc  # race-off allocation gates (the only lane running //go:build !race tests)
make lint        # drift check (read-only): mod tidy + gazelle + gofumpt + ktn-linter + alloc-lane coverage + guard
make guard       # sdkguard over the SDK's own tree at invariant level (ADR 0033)
make bench       # regenerate codec BENCH.md from real Go benchmarks
```

## Benchmarks

Kernel hot-path benchmarks live next to their source as
`internal/kernel/*/*_bench_test.go`. Run them via:

```bash
make sdk-bench          # steady-state numbers → .bench.out (benchstat-friendly)
make sdk-bench-profile  # CPU / mem / block / mutex profiles → profiles/
make sdk-bench-compare  # benchstat A/B comparison vs a recorded .bench.main.out
```

See `docs/BENCHMARK-TEMPLATE.md` for the bench conventions (white-box package,
`b.Loop()`, mandatory `b.ReportAllocs()`, `_Parallel` variants, and the
allocation budgets the CI gate enforces).

Those budgets are checked by the race-off allocation lane — `make test-alloc`,
whose target list lives in `tools/alloc-lane-targets.txt`. A test carrying
`//go:build !race` is invisible to the race suite, so that lane is its only
gate; `scripts/pre-commit/check-alloc-lane-coverage.sh` fails the build if such
a test exists in a package the lane does not cover.

## License

MIT — see [LICENSE](./LICENSE) for the full text.
