# ADR 0160 — every service has a core, and a code keeps its value when its declaration moves

- **Status**: Accepted
- **Date**: 2026-10-03
- **Deciders**: SDK maintainers
- **Amends**: [ADR 0005](0005-sdk-error-codes-dotted-quad.md) §Layout (what `LL` records), [ADR 0035](0035-pp-range-ownership-enforcement.md) (where a range's declaration lives, and a second check), [ADR 0047](0047-sdk-net-websocket.md) (the WebSocket wire format in `internal/core/net`), [ADR 0051](0051-sdk-trace-domain.md) (the W3C format in the trace core), [ADR 0063](0063-sdk-i18n-domain.md) (the language-tag and pattern parsers in the i18n core), [ADR 0064](0064-sdk-mail-domain.md) (message validation in the mail core), [ADR 0101](0101-a-secret-shown-is-a-secret-replaced-and-the-bound-is-exact.md) §D5, [ADR 0110](0110-a-document-store-writes-one-entry-and-rests-as-one-file.md) §D1, [ADR 0139](0139-a-document-store-over-sql-joins-the-transaction-its-context-carries.md) §D2 (the document store's ports), [ADR 0121](0121-a-process-reads-its-own-profiles-and-the-attribution-is-the-callers.md) §D1, [ADR 0148](0148-a-private-socket-is-gated-by-its-directory-and-the-kernel-names-the-peer.md) (four service packages with no core)
- **Related**: [ADR 0001](0001-sdk-go-multimodule-layout.md) (the layers), [ADR 0074](0074-what-a-public-alias-may-point-at.md) (ownership decides where a type lives), [ADR 0141](0141-a-mail-spool-keeps-an-identifier-its-caller-minted.md) (`IsDotAtom`'s home), [ADR 0147](0147-the-framework-is-a-module-of-the-sdk-above-pkg.md) (layer `4`), [ADR 0154](0154-the-sdks-principles-are-one-charter-and-an-incidents-rule-lives-with-its-code.md) (principles 3 and 7), [ADR 0155](0155-every-layer-groups-its-packages-by-family-and-a-path-may-move-while-v0.md) (one path in every layer), [ADR 0158](0158-distribution-mechanisms-are-the-frameworks-not-the-sdks.md) (ranges that move to the framework), [ADR 0159](0159-the-kernel-holds-what-the-domains-rewrote-and-is-published-by-nature.md) (the generic registry)

## Context

The layers were drawn as "core: domain interfaces and values; service: their
implementations" (ADR 0001), and three things on this tree depart from it.

**Four service domains have no core.** `docstore` (ADR 0110 §D1), `redact`
(ADR 0101 §D5), `profiling` (ADR 0121 §D1) and `ipc` (ADR 0148) were each
decided as "one engine, no port", so their values, their codes and their
facade's aliases all point at the engine. The reasoning held for each alone; it
leaves `ipc` with no port a test can implement a double of, and the rule "every
domain is a core and a service" with four exceptions nobody can predict.

**Error codes live in either layer, with no rule.** Counted with a search for
`.Define(` over production files: 265 declarations in `internal/core` and 318
in `internal/service`. `net`, `proc` and `config` declare every code in their
core; `logger` declares 1 in core and 36 in service, `statemachine` 1 and 21,
`sql` 5 and 17, `mail` 9 and 16, `docstore` 0 and 20. Finding a domain's codes
means searching two layers, and `codeRangeOwners` (ADR 0035) names
directories in both.

**Mechanism lives in the core.** `internal/core/net` holds the WebSocket frame
codec (`ParseWSFrameHeader`, `ApplyWSMask`, `AppendWSFrame`) and the SSE
encoder — 1 156 of its 2 627 production lines; `internal/core/trace` parses `traceparent` and
`tracestate`; `internal/core/i18n` parses language tags and compiles message
patterns; `internal/core/mail` validates addresses and headers. Each is a
parser or an encoder of a wire format: what one implementation does, not what
every implementation must accept.

One belief stood in the way of fixing the second point: that moving a
`Define` from service to core changes its code, because `LL` is the layer. It
does not. Codes are literal constants — `CodeProbeFailed errs.Code =
0x00_03_42_08` — and nothing derives `LL` from a directory. The tree already
shows it: every `third-party/` range is `LL = 3`, a service layer it is not in,
and the framework's are `LL = 4` (ADR 0147).

## Decision

### 1. Every service domain has a core

`internal/core/docstore`, `internal/core/ipc`, `internal/core/profiling` and
`internal/core/redact` are created, holding their domain's codes and values,
and the ports a second implementation or a test double needs — for `ipc`, the
listener, the dialer and the peer. The list of service domains and the list of
core domains are the same list, and a check fails the build when they differ.

### 2. Every code is declared in the core, at the service's path

Every `errs.Define` of a service package moves to the core package at the same
path relative to its layer (ADR 0155), in that package's `codes.go` and
`errors.go`; the service only uses them. A check refuses `errs.Define` in a
production file under `internal/service`. A domain's codes are then in one
place, and its facade's sentinels all alias the core.

### 3. A code's value never changes when its declaration moves

`LL` records the layer that **allocated** a range, not the directory its
declaration lives in today. A service range declared in the core keeps
`0.3.PP.*`; a range that moves to the framework keeps its `0.2.*` or `0.3.*`
(ADR 0158); a `third-party/` range stays `0.3.*`. A value is a wire contract —
a dashboard, an alert rule or a client branches on it, and `MaskByLayer` routes
by it — so renumbering a code to match its new directory is the one change a
move must not make, and `codeRangeOwners`' own comment already forbids it.

`codeRangeOwners` keeps its keys and changes the directories it maps them to;
`docs/error-codes.yaml` is regenerated (`make error-codes`) and shows the same
values under new package paths.

### 4. A wire format is a mechanism, and lives in the service

The core keeps what a second implementation would have to accept (ADR 0074):
ports, value types and their own invariants, and codes. Reading or writing a
wire format is a mechanism and moves to the service — the WebSocket frame codec
and the SSE encoder (`net`), `traceparent` and `tracestate` parsing (`trace`),
language-tag parsing and pattern compilation (`i18n`), address and header
validation with the dot-atom grammar ADR 0141 homed in `core/mail` (`mail`).
The values those parsers produce — an opcode, a close code, a span context, a
tag, an address — stay in the core.

A registry stays in the core: it is how a port is reached, not how one
implementation works, and it becomes an instance of ADR 0159's generic
registry.

## Consequences / Semantics

- **Implemented by the reorganisation series**: the four new core packages,
  the move of every service `Define` to its core path, the two checks (equal
  domain lists; no `Define` under `internal/service`), the `codeRangeOwners`
  and `//:audit_sources` edits, the regenerated `docs/error-codes.yaml`, and
  the parsers moved out of the core. This record changes no code.
- No code value changes, so no consumer, log query or alert changes either.
  `errs.HasCode` and `PrefixMatcher` keep matching what they matched.
- The core grows packages that hold only codes and values for a service that
  has one engine. That is the price of one rule with no exceptions.
- The core stays stdlib-only; the parsers that leave it were stdlib-only too.

## Breaking changes

None for code values. A `pkg/v1` alias that pointed at a service type now
points at the core type it moved to: same name, same shape, so a consumer
compiles unchanged. A function that moved from a core package to a service
package and was published keeps its facade name.

## As built

Wave 4 of the reorganisation series applied this record in seven tracks,
integrated on `refactor/sdk-tree-reorg`. Measured on `docs/error-codes.yaml`
before and after: **327 codes** changed declaring package, every one from
`internal/service` to `internal/core` — data 117, app 93, observe 69,
security 28, crypto 11, proc 9 — and the 735 (code, constant, value) triples
are identical, so §3 held. No code is declared under `internal/service` any
more, and no `codeRangeOwners` range names an `internal/service` directory:
the SDK's ranges name `internal/core` directories — 93 of them — but for the
kernel's two (`collections/ring`, `concur/batcher`) and the three `pkg/v1`
facades that own a `MM = 1` range (the logger, its `slog` bridge and the codec
umbrella).

- **§1, the four cores, at their family paths (ADR 0155).**
  `internal/core/data/docstore` holds `0.3.80.*`, the entry, version and stamp
  values, the index declaration, and two families of ports rather than one —
  `Collection[T]` and its sibling `Versioned[T]` for the file engine, whose
  calls take no context, `CollectionContext[T]` and `VersionedContext[T]` for
  the SQL engine, which cannot lose one, and `Announcer` for both; that is the
  amendment of ADR 0139 §D2, and `pkg/v1/data/docstore` publishes all five.
  `internal/core/proc/ipc` holds `0.3.91.*`, `PeerValue`, `Conn` and two frozen
  ports, `Listener` (`Accept`/`Addr`/`Close`/`Path`/`Refused`) and `Dialer`
  (`Dial`); `Config` stays in the engine (ADR 0074).
  `internal/core/observe/profiling` holds the values and `0.3.89.*` and no
  port: one engine, the runtime's, and the attribution a fold applies is a
  parameter (ADR 0121 §D1). `internal/core/security/redact` holds `0.3.73.*`,
  `DocumentValue`, the shared constants and the `Redactor` port, frozen at
  `Name`/`Text`/`JSON`/`Value`/`Attrs`.
- **§2, the declarations.** Every engine's `Define` moved to the core package
  at its path, in `codes.go` / `errors.go`. Where an engine package declared
  codes beneath its domain, the core grew a codes-only package at the same
  path: the logger's fourteen (`observe/logger/middleware/*`, `sink/*` and
  `writer/{journald, nettransport, rotfile}`), the codecs' eighteen
  (`data/codec/<format>`, `strictjson` and `jsonpatch` included), `app/mail/spool`
  and `crypto/key/jwk`. A value whose methods read or write the format stayed
  with its engine — the codecs' value types, the JWK key types — because Go
  cannot split a type from its methods, and only codes and sentinels were
  moved for them. A moved sentinel's `Private` still names the engine that
  raises it, deliberately: no `Reason`, `Public` or value changed.
- **§4, the mechanisms.** The WebSocket frame codec, close payload, handshake
  digest and UTF-8 check went to `service/net/websocket`, and the SSE encoder
  to `service/net/sse`; `traceparent` / `tracestate` reading and writing and
  `Inject` / `Extract` to `service/observe/trace`, the core keeping the
  identifiers, the flags and the state list with a `StateBuilder` the parser
  fills it through; BCP 47 tag parsing and pattern compilation to
  `service/app/i18n`, the core keeping the values and their checks; message,
  header, address and attachment validation to `service/app/mail`, with the
  dot-atom grammar. The six core registries — codec, crypto, writer,
  transform, id and view — are instances of `kernel/plugin.Registry`
  (ADR 0159), each keeping its codes, reasons and fields.
- **Published shapes that changed** (v0, ADR 0040), beyond this record's
  "same name, same shape": `pkg/v1/proc/ipc.Listener` is the core port, not
  the engine's struct, and `Listen` returns it (`NewDialer` is new);
  `pkg/v1/security/redact.Redactor` is the core port and `New` returns it;
  `mail.Message.Envelope()` became `mail.EnvelopeOf(msg)`, validation having
  left the core; `sse.Event.AppendTo` became `sse.AppendEvent`; the spool left
  `pkg/v1/app/mail` for `pkg/v1/app/mail/spool`, its names without the
  `Spool` prefix; `strictjson.DecodeRequest` left for
  `pkg/v1/data/codec/strictjson/httpbody`, so the decoder links no `net/http`.
- **The checks of §1 and §2** are one guard,
  `scripts/pre-commit/check-core-symmetry.sh`, run by `make lint` and by the
  `bazel` job, its cases in `scripts/pre-commit/test-pre-commit-guards.bats`
  (each seen red against a planted violation), and listed in
  `scripts/ci-gates-check.sh`'s `GUARDS` so the step cannot be deleted
  silently. It fails when (a) a production file under `internal/service`
  declares a code — an `errs.Define` under any import name, or a `Code`
  constant that is not a re-export; (b) a service domain has no core package
  at the same path — a domain being `<family>/<name>` in `security`,
  `observe`, `data` and `app` (a family's Go-`internal` directory excepted) and
  the family itself for `crypto`, `net` and `proc`; (c) a core package
  declares a code and neither sits at an engine's path (`internal/service`
  holds a package at it or beneath it) nor, beneath a domain, owns only
  ranges `codeRangeOwners` records as the core's own (`LL = 2`). Three
  refinements of the record's wording, each stated in the script's header:
  the comparison from the core back to the service is made for the core
  packages that declare a code, so `observe/otel` — the types every signal
  shares, no engine, no code — is not reported as a domain without an
  engine; a domain's core is not exempt from (c), so a core declaring codes
  with no engine at its path fails as the other half of §1; and an engine's
  path may be the directory of the engines a family's core serves, which is
  how `crypto`, `net`, `proc`, `data/codec` and the writer registry
  (`observe/logger/writer`, ADR 0012) pass. The exception for core-allocated
  ranges is what lets `observe/logger/level` (`0.2.17.*`, out of the kernel
  since ADR 0002) stay a sub-contract of the logger, while a layer-3 range —
  an engine's — may only sit at that engine's path.

## Alternatives considered

- **Declare "mechanism without a port" as a formal exception.** Keeps four
  unpredictable exceptions, and `ipc` still has no port for a double.
- **Declare codes where they are emitted.** Today's state: no rule, two layers
  to search, and an owner table that names both.
- **Renumber moved codes to the layer they land in.** Breaks every consumer
  that branches on a value, for a symmetry nobody reads off a log line.

## Deferred

- `internal/core/net`'s PEM reading behind `NewIdentityValue` and
  `internal/core/crypto`'s box format inside `Seal` / `Open` stayed in the
  core, judged value construction rather than a wire format; a stricter
  reading of §4 would move them.
- The JWK value types (`KeyValue`, `Set`, `Type`, `Curve`) stayed in
  `service/crypto/key/jwk`, which the token facade still aliases; the two
  enums could move to the core.

## References

- `internal/kernel/errs/registry_ownership_external_test.go` — `codeRangeOwners`,
  and its comment: never renumber a published code to make the table fit.
- `framework/internal/service/selfupdate/codes.go` — `CodeProbeFailed`, a literal,
  `0x00_03_42_08`: a layer-3 value declared in the framework since ADR 0158.
