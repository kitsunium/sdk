<!-- updated: 2026-10-05T12:00:00Z -->
# pkg/v1/collections/ring/

## Purpose

Public facade for `internal/kernel/collections/ring` (ADR 0159 §4, ADR 0006):
a bounded lock-free queue for exactly ONE producer and ONE consumer, whose
write refuses when full and whose read refuses when empty — neither ever
blocks. An alias, one forwarding constructor and the three sentinels — no
behaviour of its own. Stdlib-only → dep-light; cross-OS portable.

It is the buffer behind the asynchronous logger, published by ADR 0159 §4
beside the other kernel primitives so a program can reach it through
`pkg/v1`.

## Surface

| Symbol | Notes |
|---|---|
| `Queue[T]` | alias of the INTERFACE `TryWrite` / `TryRead` / `Capacity` / `Len` — frozen at four methods by this publication (ADR 0039, ADR 0159 §5) |
| `New[T](capacity)` | `(Queue[T], error)`; the storage is allocated once, here |
| `Full` | `0.1.3.1` — `TryWrite` on a full ring |
| `Empty` | `0.1.3.2` — `TryRead` on an empty ring |
| `CapZero` | `0.1.3.3` — `New` with a capacity that is not positive |

That is the complete exported surface of `internal/kernel/collections/ring`
except the three `Code*` constants: a caller matches the sentinels with
`errors.Is`, or by code through `errs.CodeOf(ring.Full)` and
`errs.HasCode` — the values stay the kernel's (ADR 0160 §3).

## Published single-producer, and why that is safe

ADR 0159 §5 asks that a primitive be widened BEFORE it is published, and §2
plans a multi-producer mode for this ring that has not landed. It is
published ahead of that mode, with the other §4 aliases, because the only
thing publication freezes is `Queue`'s method set, and a multi-producer ring
answers the same four methods: that mode arrives as another constructor
returning this `Queue`, never as a change to it. The package comment says so
to the consumer, and ADR 0159's "As implemented" records the order.

Until it lands, the framework keeps its own lock-free multi-producer ring in
`framework/telemetry`: this one is single-producer, and the exporter's
`Emit` is called from any goroutine of a product, concurrently.

## Conventions

- **The single-producer, single-consumer contract is the first thing the
  package comment says**, and the example names which goroutine does what:
  concurrent producers corrupt the queue, they do not merely slow it.
- **`Len` counts across the wrap.** The publication went with a kernel fix:
  `Len` was wrong for a wrapped ring whose slot count is not a power of two
  (see the kernel `CLAUDE.md`), and `TestTheRingRefusesInsteadOfBlocking`
  walks a capacity-2 ring round after round so the public surface would catch
  it again.
- **Methods are documented on the alias**, as the interface's own comments
  are not rendered for an alias (ADR 0138).
- The package comment is in `doc.go`, which kit writes from the design
  (`design/kernel/collections.yaml`, ADR 0167): edit the design, run `kit gen`, then `make api`
  and `make docs-readme`. `README.md` is written by `tools/genindex` from
  `docs/api`; do not hand-edit it.

## Do NOT

- Add a fifth method to `Queue`. A new capability is a sibling interface
  reached by type assertion (ADR 0039).
- Add a blocking variant here: a buffered channel is that queue, and the
  package comment sends the reader to it.
- Reach for `internal/kernel/collections/ring` in this package's tests.

## Generated

`facade_gen.go` is kit's (ADR 0166): every alias, re-exported constant and variable, and forwarder of this package is declared in the `facade:` of `design/kernel/collections.yaml`, doc comments included, and kit writes it. Where this file names another file as holding one of them, read `facade_gen.go`: `doc.go` holds the package comment, which kit writes from the design (ADR 0167), and the hand-written files the declarations of their own. Change a re-export, or its doc comment, in the design and run `kit gen` (then `make api` and `make docs-readme`); `make api-check` fails on a `facade_gen.go` edited by hand.

## Verification

```
bazel test --config=race //pkg/v1/collections/ring:ring_test
cd pkg && GOWORK=off go test -race ./v1/collections/ring/
```

`ring_external_test.go` is `package ring_test`: `CapZero` by `errors.Is` and
by code, four rounds of fill-to-`Full` and drain-to-`Empty` on a capacity-2
ring with `Len` checked at both ends, ten thousand items through one producer
and one consumer under the race detector — plus `ExampleNew`.
