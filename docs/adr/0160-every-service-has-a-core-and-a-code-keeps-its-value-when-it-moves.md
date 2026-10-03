# ADR 0160 — every service has a core, and a code keeps its value when its declaration moves

- **Status**: Accepted
- **Date**: 2026-10-03
- **Deciders**: SDK maintainers
- **Amends**: [ADR 0005](0005-sdk-error-codes-dotted-quad.md) §Layout (what `LL` records), [ADR 0035](0035-pp-range-ownership-enforcement.md) (where a range's declaration lives, and a second check), [ADR 0047](0047-sdk-net-websocket.md) (the WebSocket wire format in `internal/core/net`), [ADR 0051](0051-sdk-trace-domain.md) (the W3C format in `internal/core/trace`), [ADR 0063](0063-sdk-i18n-domain.md) (the language-tag and pattern parsers in `internal/core/i18n`), [ADR 0064](0064-sdk-mail-domain.md) (message validation in `internal/core/mail`), [ADR 0101](0101-a-secret-shown-is-a-secret-replaced-and-the-bound-is-exact.md) §D5, [ADR 0110](0110-a-document-store-writes-one-entry-and-rests-as-one-file.md) §D1, [ADR 0121](0121-a-process-reads-its-own-profiles-and-the-attribution-is-the-callers.md) §D1, [ADR 0148](0148-a-private-socket-is-gated-by-its-directory-and-the-kernel-names-the-peer.md) (four service packages with no core)
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

## Alternatives considered

- **Declare "mechanism without a port" as a formal exception.** Keeps four
  unpredictable exceptions, and `ipc` still has no port for a double.
- **Declare codes where they are emitted.** Today's state: no rule, two layers
  to search, and an owner table that names both.
- **Renumber moved codes to the layer they land in.** Breaks every consumer
  that branches on a value, for a symmetry nobody reads off a log line.

## Deferred

None.

## References

- `internal/kernel/errs/registry_ownership_external_test.go` — `codeRangeOwners`,
  and its comment: never renumber a published code to make the table fit.
- `internal/service/selfupdate/codes.go` — `CodeProbeFailed`, a literal.
