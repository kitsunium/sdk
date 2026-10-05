// Package encoder provides concrete Encoder implementations (Text today;
// NDJSON and JSON to land in follow-up commits). The Encoder interface
// itself lives in internal/core/observe/logger — this package is implementation-
// only, exposing the interface via a type alias so existing call sites
// that import "service/observe/logger/encoder".Encoder keep compiling.
//
// Per ADR 0005 hexagonal layering: core/ holds ports, service/ holds
// adapters. Previously Encoder lived here, creating an asymmetry where
// Sink lived in core/ and Encoder lived in service/ even though both are
// ports of the Handler.
//
// Package encoder — implements jsonEncoder, the structured single-line JSON
// encoder that renders a record as one encoding/json-compatible object per
// line: {"ts":…,"level":…,"msg":…,<flat attrs>}\n. Lives beside the text
// encoder so the format and the transport (Sink) evolve independently. The
// byte writer is hand-rolled (append-based, no reflection, no json.Marshal)
// so the hot path stays allocation-free.
//
// Package encoder — implements TextEncoder, the default human-readable
// encoder that renders a record as "TIME LEVEL msg key=val key=val …\n".
// Lives in its own package so the format and the transport (Sink) can
// evolve independently.
//
// Package encoder — renders the record timestamp, the one field every encoded
// line carries and the single most expensive thing either encoder does.
//
// A CPU profile of a bare emit attributed 34.6 % of the WHOLE call —
// builder, handler, encoder and sink together — to time.Time.AppendFormat,
// with time.nextStdChunk and time.appendInt the two largest flat entries under
// it. That is the generic formatter re-parsing the layout string, chunk by
// chunk, on every single log record, to reach a result that never varies in
// shape. appendTimestamp writes the same bytes with fixed offsets instead, and
// is measured at 6.0× the stdlib call on a UTC instant and 5.0× on an offset
// zone — `BenchmarkAppendTimestamp{,Zoned}` against their `…Stdlib` controls,
// reported in internal/service/observe/logger/encoder/BENCH.md §5.2.
//
// It is a REPLACEMENT for one exact layout, not a general formatter: anything
// it cannot render identically falls back to AppendFormat rather than
// approximating. TestAppendTimestampMatchesAppendFormat pins the equivalence
// across four zones and a hundred thousand instants each.
package encoder
