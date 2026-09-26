# ADR 0134 — a program links the codecs it imports

- **Status**: Accepted
- **Date**: 2026-09-26
- **Deciders**: SDK maintainers
- **Related**: [ADR 0003](0003-sdk-codec-package.md) (the codec registry), [ADR 0021](0021-sdk-codec-bson.md) (BSON over the MongoDB driver), [ADR 0102](0102-a-document-somebody-else-wrote-is-decoded-one-way-or-not-at-all.md) (a codec-tree package with its own facade)

## Context

`config.FileSource` / `config.FSSource` and `i18n.LoadFS` dispatch through the
codec registry by format name, and they link no codec themselves: measured,
`go list -deps ./v1/config/` names `internal/core/codec` and no codec package.
Something must register the format. The only public way was
`import _ "github.com/kitsunium/sdk/pkg/v1/codec"`, which blank-imports all
sixteen service codecs, so a framework reading YAML configuration links BSON
with `go.mongodb.org/mongo-driver`, CBOR, MessagePack, TOML — every product it
builds. A program outside this repository cannot import
`internal/service/codec/<format>` itself.

## Decision

`pkg/v1/codec/json`, `pkg/v1/codec/yaml` and `pkg/v1/codec/toml`: each
blank-imports its one service codec and nothing else, and exports `Format`, an
untyped constant (`"json"`, `"yaml"`, `"toml"`), so it goes into
`config.FSSource`'s `string` and `i18n.LoadFS`'s `codec.Format` without a
conversion. `pkg/v1/codec` keeps registering everything.

Registration needs no guard: a service codec registers itself in its own
package initialisation, which Go runs once however many packages import it, so
a program importing a per-format facade AND `pkg/v1/codec` registers the format
once. `codec.Register` still panics on a true duplicate, and that is what makes
the co-import test meaningful: it would not reach its first line.

## Consequences

- The framework imports `pkg/v1/codec/json`, `yaml` and `toml` for its
  configuration and catalogues and links `yaml.v3` and `go-toml/v2`, not the
  MongoDB driver.
- Measured with `go list -deps`: `pkg/v1/codec/yaml` depends on the core
  registry, four kernel packages, `internal/service/codec/yaml` and
  `gopkg.in/yaml.v3`; `toml` on `go-toml/v2` instead; `json` on the standard
  library alone.

## Breaking changes

None. Three packages are new; `pkg/v1/codec` is unchanged.

## Alternatives considered

- **Split `pkg/v1/codec` into a dispatch package without blank imports.** The
  dispatch is already reachable through `config`, `i18n` and the core registry;
  splitting the published package would move every consumer for a benefit the
  per-format imports already give.
- **All sixteen at once.** Three is what a consumer asks for today; the pattern
  is one file per format, and the others follow when one is wanted alone.
- **Marshal and Unmarshal on each facade.** The formats are reached through the
  registry by everything that reads them; a per-format copy of the dispatch
  would be a second API for the same call.

## Deferred

- The other thirteen formats, one package each, when a consumer needs one
  alone.

## Verification

- `pkg/v1/codec/{json,yaml,toml}/*_external_test.go` — each test binary imports
  one facade and `pkg/v1/config`: it decodes its format through
  `config.FSSource`, is refused BSON with `SourceFailed`, finds exactly its
  format in the registry, links none of the other formats' modules (read from
  `debug.ReadBuildInfo`, under `go test`), and `go list -deps` of the facade
  names no other codec package (where a go tool is on PATH).
- `pkg/v1/codec/formats_external_test.go` — the four imported together: each
  format registered once, and dispatch through `codec.Unmarshal` intact.
