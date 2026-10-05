// Package telemetry — the exporter: a bounded ring producers never wait on,
// drained to the clients attached to a private socket, which it never reads.
//
// Package telemetry — the compile-time proof that Exporter is an Emitter.
//
// Package telemetry is the port a running product reports what it does on,
// and the exporter that hands it to a tool attached to the product (ADR 0149).
//
// It is not the trace domain. pkg/v1/observe/trace builds spans a backend stores and
// carries its context through context.WithValue; this port carries nothing
// through a context, builds nothing a caller keeps, and costs a producer one
// fixed-size copy into a bounded ring — zero allocations, pinned by a test
// (testing.AllocsPerRun) and measured in BENCH.md:
//
//	emitter.Emit(&telemetry.Event{Kind: telemetry.KindSpan, Op: telemetry.OpRequest,
//		Node: ref, Start: start, Duration: d, Outcome: telemetry.OutcomeOK})
//
// # A closed schema
//
// An [Event] holds numbers only: enumerations ([Kind], [Op], [Outcome]), a
// dotted-quad error code, instants, identifiers a trace already carries, and
// NODE REFERENCES — indexes into the table of node IDs the exporter sends once,
// in its handshake, each one checked against the ID grammar
// (framework/model.ParseID). There is no free text, so there is no field a
// person's name, an address or a request body could travel in: the schema
// itself is what keeps personal data out, in every environment.
//
// # Inert unless configured
//
// [Nop] is the emitter a product holds by default: Emit returns at once. An
// [Exporter] exists only when the product was started with one, and it only
// EMITS — it never reads what a client sends, so nothing reaches the product
// through it. It listens on a private socket (pkg/v1/proc/ipc, ADR 0148): the
// product's account and the UIDs and GIDs the deployment names, under the
// runtime directory; a stale socket is replaced, a live one refused. A client
// connects, reads one handshake line — product, binary, role, instance,
// revision, model version, design digest, the node table, the first sequence
// number — then fixed-size records ([RecordSize] bytes each) for as long as
// it reads. A slow client loses records, counted; the product never waits.
//
// Package telemetry — the interfaces: what a producer emits to, and what the
// exporter needs of its listener and of a client.
package telemetry
