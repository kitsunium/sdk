# ADR 0030 — stdout is a protocol channel: no SDK default writes to it

- **Status**: Accepted
- **Date**: 2026-09-03
- **Deciders**: SDK maintainers
- **Related**: ADR 0007 (bump semantics), ADR 0012 (writer registry), ADR 0015 (writer taxonomy + default-active writers), ADR 0027 (metrics domain)
- **Amends**: ADR 0027 §Decision 2 (the text exporter's registered destination), ADR 0015 §D3 (the console writer's default stream)

## Context

Two SDK surfaces defaulted to `os.Stdout`, and both were reported by a consumer
building a daemon that speaks a protocol on stdout:

1. `internal/service/metrics/exporter_text.go` registered its text `Exporter` on
   `os.Stdout` at package load. Importing `pkg/v1/metrics` — for a counter, for
   the type aliases, transitively — armed a writer on stdout. A single
   `Export("text", snap)` anywhere in the process then interleaved
   `counter requests 7` into the stream.
2. `corewriter.ConsoleStdout` was the zero value of `ConsoleStream`, so
   `logger.ConsoleConfig{}` — a config a caller writes when it has no opinion
   about the stream — targeted stdout.

For a great many Go programs stdout is not a display, it is a channel: a
JSON-RPC server in stdio mode (MCP), `cmd | jq`, a filter in a shell pipeline, a
program whose output another program parses. Writing diagnostics there does not
produce noisy output, it produces a **corrupt stream** — and the failure is
attributed to the protocol, not to the import that armed the writer.

Both defaults were reachable without naming a stream. That is what makes them
traps: the caller who is thinking about the choice picks correctly either way;
the caller who is not thinking about it gets the dangerous one. A zero value is
the choice made by someone who has not yet learned the question exists.

## Decision

**No SDK default writes to `os.Stdout`.** Concretely:

1. The registered `text` metrics exporter writes to `os.Stderr`.
   `NewTextExporter(name, os.Stdout)` remains available: stdout becomes
   opt-in, named at the call site, and never a consequence of an import.
2. `ConsoleStderr` is the zero value of `corewriter.ConsoleStream`, so
   `ConsoleConfig{}` targets stderr. `ConsoleStdout` keeps its name and its
   behaviour; only its numeric value and its default-ness change.
3. The `console` writer's `Decode` maps an **absent** `target` key and an
   explicitly empty `target: ""` to the same place as the zero value — stderr.
   A named `target: "stdout"` is honoured, because it is a choice.

The rule generalises past these two cases: when an SDK surface must pick a
stream without being told, it picks stderr. Diagnostics on stderr are at worst
noise; diagnostics on stdout are data corruption. The asymmetry of the two
failure modes is the whole argument.

## Consequences / Semantics

- **"Unspecified" now means stderr on every surface that has a default.**
  `ConsoleConfig{}`, an absent `target` key, an explicitly empty `target: ""`,
  and the exporter that a bare import registers all resolve to `os.Stderr`.
  Naming a stream still selects it — `ConsoleStdout`, `target: "stdout"`, and
  `NewTextExporter(name, os.Stdout)` behave exactly as before.
- **The default is a constant, not a probe.** It does not depend on whether
  stdout is a terminal, on an environment variable, or on anything else decided
  at run time; the same binary always starts in the same place.
- **`ConsoleStdout` keeps its name and its behaviour.** Only its numeric value
  and its default-ness change, so the pair remains complete and the
  out-of-range guard in `Open` still rejects anything outside it.
- The guard against regression is a test per surface, not prose: each pins the
  default destination by identity and fails naming the stream.

## Breaking changes

Two behavioural changes land under this decision — the registered `text`
metrics exporter moves from `os.Stdout` to `os.Stderr`, and the `ConsoleStream`
zero value moves from `ConsoleStdout` to `ConsoleStderr`. Both alter the
observable behaviour of caller code that did not change, which ADR 0007 §Bump
semantics classifies as a **minor bump, not a patch** ("behavioral change in
`internal/**` observable through `pkg/<major>` → minor"); the commits that land
them carry the `Release-bump: minor` trailer.

- A consumer relying on the old destinations must now say so: `os.Stdout` passed
  to `NewTextExporter`, or `ConsoleConfig{Stream: ConsoleStdout}` /
  `target: "stdout"`. Both are one token, and both are now visible in review —
  which is the point.
- **The numeric values of `ConsoleStdout` and `ConsoleStderr` swap.** This was
  audited before the change and is safe here: `ConsoleConfig` carries no
  `json`/`yaml`/`toml` struct tags and is never a codec unmarshal target (the
  config path reaches the writer through `WriterEntryConfig.Options
  map[string]any`, and `consoleFactory.Decode` is text-keyed and rejects a
  non-string `target`); `ConsoleStream` implements no `String`, `MarshalText`,
  `MarshalJSON` or `MarshalBinary`; nothing indexes an array or map by the
  value; and `pkg/v1/logger.StreamStdout`/`StreamStderr` are defined by
  reference to the core constants, so they follow automatically. Had any of
  those been false — an enum crossing CBOR, msgpack or ASN.1 changes meaning
  silently for a consumer re-reading old data — the correct move would have been
  a distinct zero-value sentinel rather than a reordering.

## Alternatives considered

- **Register no metrics exporter at all on import.** Rejected: it makes
  `Export("text", …)` fail for every existing caller, which is a breaking change
  in exchange for a safety property that stderr already delivers.
- **Keep stdout and document the hazard.** Rejected: the trap is precisely that
  the dangerous path is the one a caller reaches without reading anything. A
  documented mine is still a mine, and the import that plants it is not visible
  at the call site.
- **Add a `ConsoleUnset` sentinel as the zero value and reject it.** Rejected
  for the console: it turns `ConsoleConfig{}` from valid-and-safe into invalid,
  and the "zero value is valid" contract is worth more than the extra
  explicitness. It remains the right answer for any enum whose numeric values
  *are* serialised — see the audit above.
- **Detect whether stdout is a terminal and switch on that.** Rejected: a
  runtime probe makes the destination depend on how the process was launched, so
  the same binary logs to two different places and neither is predictable. A
  default must be a constant.

## Why not a supersession of ADR 0027 / 0015

Neither decision is reversed. ADR 0027 still puts a stdlib text exporter behind
the registry; ADR 0015 still makes console a default-active writer. Only the
stream each of them picks when nobody names one changes, so this ADR **amends**
them in the house style (cf. ADR 0012 amending 0005/0006, ADR 0020 amending
0005) rather than superseding them.

## Deferred

- **A mechanical guard for the rule itself.** What is enforced today is the two
  destinations, by one regression test each; the *rule* — no SDK default writes
  to `os.Stdout` — is enforced by review. An audit in the style of the `errs`
  AST registry check (or a `scripts/pre-commit/` guard) failing on a
  package-level binding of a writer to `os.Stdout` would close that gap. Not
  built here: with two subjects, the audit costs more to maintain than it
  catches. It becomes worth writing at the third surface.
- **Revisit the zero-value reordering if `ConsoleStream` ever becomes
  serialisable.** The audit under Breaking changes holds only while no numeric
  value of the enum crosses a wire or a file. Adding a `MarshalText`,
  `MarshalJSON`, `MarshalBinary` or codec participation to `ConsoleStream`
  would make the ordering externally visible, and the `ConsoleUnset` sentinel
  rejected under Alternatives becomes the correct shape at that point. Nothing
  in the tree enforces the precondition today.

## References

- Impl: `internal/service/metrics/exporter_text.go`, `internal/core/writer/config_console.go`, `internal/service/writer/console/console.go`.
- ADR 0027 §Decision 2 — "a stdlib text Exporter (registered to stdout on import)", the clause this ADR amends.
- ADR 0015 §D3 — default-active writers (console AND file).
- ADR 0007 §Bump semantics — why this is a minor, not a patch.
