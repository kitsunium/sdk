# ADR 0149 — a product reports numbers on a port, and its exporter only emits

- **Status**: Accepted (proposed 2026-09-28; accepted 2026-10-03, once the code it decides had shipped)
- **Date**: 2026-09-28
- **Deciders**: SDK maintainers
- **Related**: [ADR 0147](0147-the-framework-is-a-module-of-the-sdk-above-pkg.md) (the framework module; layer `4`), [ADR 0148](0148-a-private-socket-is-gated-by-its-directory-and-the-kernel-names-the-peer.md) (the private socket the exporter listens on), [ADR 0051](0051-sdk-trace-domain.md) (the trace domain this is not), [ADR 0030](0030-stdout-is-a-protocol-channel.md) (nothing armed by an import)

## Context

The platform's plan (§6, D10) wants a production process observable by a tool
an operator attaches — `kit attach` — with nothing reported by default, no
identifier of a person outside dev, and a cost a hot path can afford. The
framework had two things near it and neither fits. Its dev hub keeps spans,
payloads and log records for the Studio: rich, allocating, dev-only. The SDK's
`trace` builds OTel spans a backend stores, carries its context through
`context.WithValue`, and ships attributes — free text a person's data can
travel in.

## Decision

A new framework package, `framework/telemetry` (`0.4.3.*`).

1. **A port of numbers.** `Emitter.Emit(*Event)` takes an `Event` whose fields are
   numbers only: a closed `Kind` (span, phase), a closed `Op` (the model's span
   operations and phases, by name in `OpNames`), an `Outcome`, a dotted-quad
   code, instants, the trace and span identifiers, and NODE REFERENCES —
   indexes into the table of node IDs the handshake sends once, each checked
   against the ID grammar. No field holds text, so nothing identifying a
   person can be reported, in any environment. `Emit` does not allocate
   (`TestEmitAllocatesNothing`, race off, in the alloc lane; `BENCH.md`: 11 ns,
   3 ns for `Nop`) and never blocks.
2. **Inert unless configured at the start.** `Nop` is the default. The
   framework's `App` builds an exporter only when `KIT_TELEMETRY` (or
   `kit.Telemetry`) names a socket at the start; a CLI run never exports.
3. **The exporter only emits.** It listens on a private socket (ADR 0148):
   the product's account and the groups `KIT_TELEMETRY_GIDS` names — the
   on-call group declared at deployment —, under the runtime directory, a
   stale socket replaced and a live one refused. A client reads one JSON
   handshake line (`kit-telemetry/v1`: product, binary, role, a random
   instance, the VCS revision, `model.Version`, the design digest, the node
   table, the operations, the record size, the next sequence number) and
   then 64-byte little-endian records at fixed offsets. The exporter reads
   nothing from a client.
4. **A bounded ring, never a wait.** Producers claim a slot of a bounded ring
   (a sequence per slot, one compare-and-swap); a full ring drops the event
   and counts it (`Dropped`). One goroutine drains the ring even with no
   client, so the ring never fills because nobody listens; a client that does
   not read within 250 ms is dropped.

## Consequences / Semantics

- A record is meaningful only with its handshake: the node table and the
  operation names travel once, so a record is 64 bytes whatever the IDs'
  lengths.
- Sequence numbers are the exporter's, contiguous per process: a reader sees
  a gap as records dropped for it.
- The dev hub is unchanged and still dev-only; telemetry is the production
  path.

## Breaking changes

None: a new package; the framework's default port is `Nop`.

## Alternatives considered

- **OTLP spans from `trace`.** Refused for this path: attributes are text, the
  context travels by `WithValue`, and a span allocates.
- **A TCP port.** Refused: any account reaches loopback (ADR 0148).
- **JSON records.** Refused: an encoder allocates and a record's size would
  depend on its content; the handshake is JSON because it is sent once.

## Deferred

- The Studio's display of the exporter's own cost beside the durations (the
  plan's §6, last item) is the platform's.
- Relaying a remote exporter over `ssh -L` is operational, not code.

## Verification

```sh
GOWORK=off go -C framework test -race ./telemetry/ ./kit/
GOWORK=off go -C framework test -run TestEmitAllocatesNothing ./telemetry/   # race off
```

## References

- The platform's plan, "kit design-first" v7, §6 and D10.
