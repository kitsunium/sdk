# ADR 0156 — the public module links the standard library and nothing else

- **Status**: Accepted
- **Date**: 2026-10-03
- **Deciders**: SDK maintainers
- **Amends**: [ADR 0003](0003-sdk-codec-package.md) §M2–§M4 (YAML, TOML, CBOR and MessagePack over libraries, and the "30-dependency soft ceiling"), [ADR 0021](0021-sdk-codec-bson.md) (BSON over `mongo-driver/bson`, and the library-backed precedent it cites), [ADR 0077](0077-a-self-update-is-an-order-of-operations-and-a-product-name-is-not-part-of-it.md) (`golang.org/x/mod/semver`), [ADR 0100](0100-a-program-reads-what-it-was-built-from-and-asks-git-only-about-a-working-tree.md) (`golang.org/x/mod/module`)
- **Related**: [ADR 0012](0012-logger-writer-registry.md) (why `pkg` must stay dependency-light), [ADR 0034](0034-hcl-quarantine-rationale-corrected.md) (measure what a dependency introduces), [ADR 0063](0063-sdk-i18n-domain.md) (a named subset, everything else refused by name), [ADR 0134](0134-a-program-links-the-codecs-it-imports.md) (one facade per format), [ADR 0154](0154-the-sdks-principles-are-one-charter-and-an-incidents-rule-lives-with-its-code.md) (principles 5, 16, 24), [ADR 0157](0157-one-module-per-vendor-released-with-the-sdk.md) (the module `yaml-full` lives in), [ADR 0159](0159-the-kernel-holds-what-the-domains-rewrote-and-is-published-by-nature.md) (`kernel/semver`)

## Context

ADR 0012 kept the AWS SDK out of `pkg` so that a consumer of the logger, the
codecs and the errors would not inherit a vendor's module graph. `pkg` never
became that module. Measured on this tree, `go list -deps ./...` in `pkg`
links, beyond the SDK and the standard library:

| Module | Brought in by | For |
|---|---|---|
| `github.com/fxamacker/cbor/v2` (+ `x448/float16`) | `codec/cbor` | one Format |
| `github.com/vmihailenco/msgpack/v5` (+ `tagparser/v2`) | `codec/msgpack` | one Format |
| `github.com/pelletier/go-toml/v2` | `codec/toml` | one Format |
| `gopkg.in/yaml.v3` | `codec/yaml` | one Format, and every YAML configuration file |
| `go.mongodb.org/mongo-driver` (`bson` and its `x/` packages) | `codec/bson` | one Format |
| `golang.org/x/mod` | `proc/self`, `selfupdate`, `entitlement` | six functions: `semver.IsValid`, `Compare`, `Prerelease`; `module.IsPseudoVersion`, `PseudoVersionRev`, `PseudoVersionTime` |

`go list -m all` in `pkg` names 35 modules, `pkg` itself included. ADR 0003 accepted the first four
under a "30-dependency soft ceiling", ADR 0021 followed what it called the
library-backed precedent for BSON, and ADR 0077 and 0100 took `x/mod` for a
version comparison and a pseudo-version split. Each step was small; together
they make every consumer of `pkg/v1/codec` resolve a MongoDB driver's graph.

YAML is the sharpest case, because it is not only a Format. `config` has no
parser of its own: a YAML configuration file is decoded through the codec
registry, so whatever `codec/yaml` accepts, a service's configuration accepts —
today the whole of `yaml.v3`: anchors and aliases, tags, merge keys, several
documents in one file. A configuration file is text an operator wrote, often
copied from somewhere else; those constructs are where one document is read two
ways by two parsers.

The SDK already implements standards from their documents with the standard
library — OTLP/JSON, W3C Trace Context, RFC 6455, MIME and SMTP, the CLDR
plural charts — and already ships a NAMED subset with everything outside it
refused by name: i18n's thirteen CLDR entries (ADR 0063).

## Decision

### 1. `pkg` links the standard library and nothing else

Every module `pkg` requires is an SDK module, and every package it links is
the SDK's or the standard library's. A dependency that would enter `pkg`'s
graph needs an ADR amending this one; the alternative it must argue against is
a module of its own under `third-party/` (ADR 0157).

### 2. MessagePack, CBOR, BSON and TOML are written natively

Each is implemented with the standard library from its specification —
MessagePack's, RFC 8949, BSON 1.1, TOML 1.0.0 — in the codec tree, replacing
the library it wrapped. Each keeps its Format name, its MIME types and
extensions, its error range and its hardening bounds (BSON's size cap among
them). Where the specification leaves a choice the library made — map ordering,
integer width, how a time is encoded — the native codec makes its own, writes
it down in its `CLAUDE.md`, and the pull request that replaces the library
names every observable difference measured against it on the codec's own
suite.

### 3. YAML is a named subset; the full grammar is opt-in

The Format `yaml` becomes a native YAML 1.2.2 subset sized for configuration
(`internal/service/data/codec/yaml`; the table is what it reads and refuses,
as built):

| Read | Refused by name (code) |
|---|---|
| block mappings and block sequences, the compact `- key: value` and `- - nested` entries | anchors, `&a` (`0.3.4.3`) |
| flow mappings and flow sequences, across lines too, a single-pair mapping in a flow sequence (`[a: 1]`) and a key with no value (`{a, b: 2}`) | aliases, `*a` (`0.3.4.4`) |
| plain scalars, across lines; single-quoted and double-quoted scalars with every YAML escape but `\/` | tags, `!x`, `!!str`, `!<uri>` (`0.3.4.5`) |
| literal (`\|`) and folded (`>`) block scalars, with chomping and indentation indicators | merge keys, `<<` (`0.3.4.6`) |
| comments | a second document, `---` after content (`0.3.4.7`) |
| one document, with an optional `---` start and `...` end; a byte order mark first; CRLF line breaks | complex keys, `? k` or a flow collection as a key (`0.3.4.8`) |
| plain scalars resolved by the YAML 1.2 core schema: `true` / `false` and never `yes`, `no`, `on`, `off`; `0o` octal and `0x` hexadecimal integers; `.inf`, `.nan`; everything else a string | directives, `%YAML`, `%TAG` (`0.3.4.9`) |
| | a mapping holding one key twice (`0.3.4.10`) |
| | an integer written with a leading zero, `0644`, wherever its value matters (`0.3.4.11`) |

A refusal names the construct and the line and column it starts at, never the
value (principle 8), and carries `UNMARSHAL_FAILED` (`0.3.4.2`) in its trail.
A construct is refused rather than read some other way for the reason ADR 0063
refuses an unsupported language: a document read differently from what its
author meant, and accepted, is the failure nothing observes — which is why the
duplicate key and the leading zero, two documents YAML readers read
differently, are refused by name beside the constructs. Whatever else lies
outside the subset — a reserved indicator (`@`, `` ` ``), a tab where
indentation is, an unknown escape, a multi-line implicit key, content on the
`---` line, a `...` closing nothing — is `UNMARSHAL_FAILED` with a constant
detail naming it, and a document that is not UTF-8, carries a raw control
character, a raw U+0085, U+2028 or U+2029, or a byte order mark anywhere but
first is refused before the parse. A document is bounded at 10 MiB, 2^20 nodes,
a nesting depth of 100 and implicit keys of 1 024 runes. `Unmarshal` reads one
document; the streaming decoder reads a `---`-separated stream, one document
per `Decode`.

The full `yaml.v3` codec stays available as an opt-in module,
`third-party/codec/yaml` (ADR 0157), registering the Format `yaml-full`. It
claims no MIME type and no extension the native codec holds, so a program may
import both and the registry refuses nothing; a configuration that needs
anchors reads its file as `yaml-full`.

### 4. A native semver replaces `golang.org/x/mod`

`internal/kernel/semver` implements SemVer 2.0.0 precedence with Go's leading
`v`, validity, the pre-release part, and the Go module pseudo-version form —
recognising one, and reading its revision and its time: the six functions the
tree calls today and nothing else. It is a kernel primitive (stdlib-only,
generic) and is published as `pkg/v1/semver` (ADR 0159), because the
distribution mechanisms that compare versions move to the framework, which
reaches the SDK only through `pkg/v1` (ADR 0158).

## Consequences / Semantics

- **Implemented by the reorganisation series**: the four native codecs, the
  native YAML subset and the `yaml-full` module, `kernel/semver`, and the
  removal of the six requirements from `internal/service/go.mod`. After the
  last of them, `go list -m all` in `pkg` names the SDK's own modules only.
- **As implemented**: all of it has landed.
  - The four native codecs, each with its consumer-visible deviations listed in
    its package `CLAUDE.md` and pinned by its own suite — CBOR writes map pairs
    sorted by encoded key (RFC 8949 §4.2.1); BSON keeps the driver's v1
    mapping, gains two codes in its own range (`0.3.36.4`
    `BSON_DEPTH_EXCEEDED`, `0.3.36.5` `BSON_VALUE_INVALID`) and a facade,
    `pkg/v1/data/codec/bson`, which registers BSON alone and aliases its value types
    because a program reading BSON holds them (ADR 0074, ADR 0134); TOML's
    `LocalDate`, `LocalTime` and `LocalDateTime` are the SDK's own types,
    re-exported by `pkg/v1/data/codec/toml`, and its decoder still accepts the four
    TOML 1.1.0 relaxations the replaced library accepted while its encoder
    writes 1.0.0.
  - The native YAML subset reads and refuses exactly §3's table and keeps
    `0.3.4.1` / `0.3.4.2`; the duplicate key and the leading-zero integer are
    the two refusals by name the implementation added to the table this record
    first carried. Two differential fuzzers hold it to `yaml.v3` — whatever the
    subset accepts, `yaml.v3` reads as the same value — from
    `third-party/codec/yaml`, the opt-in `yaml-full` module (range `0.3.77.*`),
    which claims no MIME type and no extension.
  - `kernel/semver` replaced `golang.org/x/mod` in `proc/self`, and the
    framework's `entitlement` and `selfupdate` call it through
    `pkg/v1/data/semver` — in the `data` family, not at the root §4's
    reference to ADR 0159 implied (ADR 0155, as built).
  - Measured on the merged tree with `GOWORK=off`: `go list -m all` in `pkg`
    names the SDK's own four modules (`pkg`, `internal/core`,
    `internal/kernel`, `internal/service`) and nothing else, and
    `go list -deps -test ./...` reaches no package outside
    `github.com/kitsunium/sdk` — nor does it in `internal/kernel`,
    `internal/core`, `internal/service` or the framework. No `go.mod` of
    `pkg`, `internal/*` or the framework requires a codec library or
    `golang.org/x/mod`. Of the vendor modules, `third-party/codec/yaml` alone
    requires `gopkg.in/yaml.v3`, and `third-party/codec/hcl` lists
    `golang.org/x/mod` as an indirect requirement of HCL's own graph (through
    `x/tools`); the auxiliary `e2e` lists `yaml.v3` indirectly too, through the
    test helpers of a database driver its integration suites import.
- **Mechanical for the codecs only.** Each per-format facade under
  `pkg/v1/data/codec` and `pkg/v1/data/transform` — seventeen packages —
  carries `TestItLinksNoModuleOutsideTheSDK`, which reads its own test
  binary's build information and fails on a module outside
  `github.com/kitsunium/sdk`. Bazel builds without module information and
  skips it; `go test` runs it — the 32-bit lane (`test-386`) over the whole
  census, and the whole-suite steps of `e2e-cross` on macOS and Windows. No
  gate covers the rest of `pkg`: there §1 is still checked in review with
  `go list -m all`; see §Deferred.
- A service whose configuration uses a refused construct fails at load, with
  the construct and its line named, instead of loading something its author did
  not write. The way out is one import and one Format name.
- The per-format facades of ADR 0134 stay: a program that reads TOML still
  links one codec, not sixteen.
- `third-party/codec/{hcl,protobuf}` are unchanged: HCL and Protobuf were
  quarantined from the start (ADR 0022, 0023).

## Breaking changes

- A YAML document using anchors, aliases, tags, merge keys, several documents,
  complex keys or directives, holding a key twice or writing an integer with a
  leading zero is refused by the Format `yaml` — and so by every YAML
  configuration file — where `yaml.v3` accepted it. A document `yaml.v3` read
  under YAML 1.1 rules reads by the core schema instead: `yes` / `no` / `on` /
  `off` into a `bool` are refused, `1_000`, `0b101` and a timestamp are text in
  an untyped target, a float decodes into an integer only when it is whole,
  `UnmarshalYAML(*yaml.Node)` is refused, and a mapping with non-string keys
  reads as `map[string]any`; `internal/service/data/codec/yaml/CLAUDE.md` lists them.
  `yaml-full` reads every one of them as before.
- Bytes written by the native MessagePack, CBOR, BSON and TOML codecs may differ
  from the library's wherever the specification leaves a choice; each
  difference is named in the pull request that makes it and in the codec's
  `CLAUDE.md`. Values round-trip.
- Permitted while the module is v0 (ADR 0040), and stated here rather than
  discovered.

## Alternatives considered

- **Keep the libraries.** Every consumer of `pkg/v1/codec` keeps resolving 35
  modules, a database driver's among them, for five Formats.
- **Move the four library codecs to `third-party/` modules, as HCL was.**
  `pkg` would be stdlib-only, but MessagePack, CBOR, BSON and TOML would leave
  the default codec set. Their specifications are small enough to own; YAML's
  full grammar is not, which is why it alone keeps an opt-in library module.
- **A complete native YAML.** The full grammar is large and its parsers have a
  long history of disagreeing with each other; what a configuration needs is
  the table above, and the rest is refused by name rather than half-supported.
- **Vendor the libraries' source into the tree.** The requirements leave
  `go.mod`, the code and its maintenance stay, and its licence and provenance
  move into the repository.
- **Keep `x/mod` for its six functions.** It is maintained by the Go team and
  stdlib-pure; it is also a module in every consumer's graph for a comparison
  the SDK can write in a few hundred lines, and the framework could not reach
  it through `pkg/v1` anyway.

## Deferred

- **A gate for §1 over the whole of `pkg`**: a lint step asserting that
  `go list -deps ./...` in `pkg` reaches no module outside the SDK and the
  standard library, where today only the codec facades' own tests look.
- Formats ADR 0003 and 0023 deferred (Avro, Cap'n Proto) stay deferred.

## References

- `go list -deps ./...` and `go list -m all` in `pkg`, on the tree this record
  was written against — the table in §Context.
- `internal/service/app/config/file_source.go` — a configuration file is decoded
  through the codec registry.
- [MessagePack specification](https://github.com/msgpack/msgpack/blob/master/spec.md),
  [RFC 8949 (CBOR)](https://www.rfc-editor.org/rfc/rfc8949),
  [BSON 1.1](https://bsonspec.org/spec.html),
  [TOML 1.0.0](https://toml.io/en/v1.0.0),
  [YAML 1.2.2](https://yaml.org/spec/1.2.2/),
  [SemVer 2.0.0](https://semver.org/spec/v2.0.0.html),
  [Go modules reference — pseudo-versions](https://go.dev/ref/mod#pseudo-versions).
