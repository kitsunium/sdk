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

The Format `yaml` becomes a native YAML 1.2 subset sized for configuration:

| Supported | Refused by name |
|---|---|
| block mappings and block sequences | anchors (`&a`) |
| flow mappings and flow sequences | aliases (`*a`) |
| plain, single-quoted and double-quoted scalars | tags (`!x`, `!!str`) |
| literal (`\|`) and folded (`>`) block scalars, with chomping | merge keys (`<<`) |
| comments | multiple documents (a second `---`, or `...`) |
| one document, a leading `---` accepted | complex keys (`? `) |
| plain scalars resolved by the YAML 1.2 core schema | directives (`%YAML`, `%TAG`) |

A refusal names the construct and the line it starts on, never the value
(principle 8). A construct is refused rather than read some other way for the
reason ADR 0063 refuses an unsupported language: a document read differently
from what its author meant, and accepted, is the failure nothing observes.

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
- **Not yet mechanical**: no gate fails when a vendor module enters `pkg`'s
  graph. Until one does, §1 is checked in review with `go list -m all`; see
  §Deferred.
- A service whose configuration uses a refused construct fails at load, with
  the construct and its line named, instead of loading something its author did
  not write. The way out is one import and one Format name.
- The per-format facades of ADR 0134 stay: a program that reads TOML still
  links one codec, not sixteen.
- `third-party/codec/{hcl,protobuf}` are unchanged: HCL and Protobuf were
  quarantined from the start (ADR 0022, 0023).

## Breaking changes

- A YAML document using anchors, aliases, tags, merge keys, several documents,
  complex keys or directives is refused by the Format `yaml` — and so by every
  YAML configuration file — where `yaml.v3` accepted it. `yaml-full` reads it.
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

- **A gate for §1**: a lint step asserting that `go list -deps ./...` in `pkg`
  reaches no module outside the SDK and the standard library.
- Formats ADR 0003 and 0023 deferred (Avro, Cap'n Proto) stay deferred.

## References

- `go list -deps ./...` and `go list -m all` in `pkg`, on the tree this record
  was written against — the table in §Context.
- `internal/service/config/file_source.go` — a configuration file is decoded
  through the codec registry.
- [MessagePack specification](https://github.com/msgpack/msgpack/blob/master/spec.md),
  [RFC 8949 (CBOR)](https://www.rfc-editor.org/rfc/rfc8949),
  [BSON 1.1](https://bsonspec.org/spec.html),
  [TOML 1.0.0](https://toml.io/en/v1.0.0),
  [YAML 1.2.2](https://yaml.org/spec/1.2.2/),
  [SemVer 2.0.0](https://semver.org/spec/v2.0.0.html),
  [Go modules reference — pseudo-versions](https://go.dev/ref/mod#pseudo-versions).
