<!-- updated: 2026-11-10T00:00:00Z -->
# internal/service/net/http2/

## Purpose

The loop engine's sibling HTTP/2 profile (ADR 0221): pure functions over
`[]byte`, no I/O, the shape `http1` has. `ParseFrameHeader` and the `Append`
family read and write RFC 9113's frames; `Decoder` and `Encoder` are HPACK
(RFC 7541), written in the SDK because the standard library exports none. `Conn`
and `Lane` are one connection's protocol state — streams, windows, SETTINGS, the
GOAWAY sequence — as a state machine with no goroutine and no pointer in its
per-connection state. `Feed` reads whole frames and reports what the adapter
must do; nothing of the codec touches a socket or a connection table (ADR 0186
§D3). `HandleLoop` (`loop*.go`) is the adapter onto the loop engine, published
by `pkg/v1/net/http2` (#961): a `corenet.LoopHandler` that reads each
connection's first bytes, and that hands a connection that does not open with
the preface to an embedded `http1.HandleLoop`, one chain serving both.

Its values and sentinels are `internal/core/net/http2`'s (ADR 0160): the frame
header, the bounds (`LimitsValue`), the wire error codes. Requests and
responses are `internal/core/net`'s `HTTP1RequestValue` and
`HTTP1ResponseValue`, so the chain's links serve one value for both versions.

## Contents

| File | Surface |
|---|---|
| `conn.go` | `Conn`, `Lane` and its arenas (streams by index, HPACK tables by chunk, hold buffers), `NewLane`, `Open`, `Close`, `Events`, `EndBatch`, `Decode`, `Closing`, `Done`, `Event`, the frame budget and the Rapid Reset budget |
| `conn_feed.go` | `Feed` and the frame dispatch; the preface and the server's SETTINGS; the stream list (`find`, newest first), `finishIfDone` |
| `conn_data.go` | DATA on the stream table: windows, body bound, padding credited back |
| `conn_headers.go` | HEADERS plus CONTINUATION as one sequence, read whole, bounded, its walk resumed across reads; opening a stream; refusing the 101st; trailers |
| `conn_control.go` | PRIORITY, RST_STREAM (the Rapid Reset budget), PING, GOAWAY, PUSH_PROMISE (refused), WINDOW_UPDATE |
| `conn_settings.go` | SETTINGS: ACK moves the decoder's table bound; INITIAL_WINDOW_SIZE moves every open stream's window, negative allowed, overflow an error |
| `conn_send.go` | `Headers`, `Send` (DATA bounded by both windows and the peer's frame size, the remainder held and resumed on credit), `Consume`, `AppendCredits` (at most one WINDOW_UPDATE per stream and one for the connection), `GoAway`, `Abort`, `ResetStream`, `OpenStreams` |
| `loop.go` | `HandleLoop`, `NewLoopHandler`, `LoopHandler` and its callbacks; the preface sniff (a connection's verdict, once, per connection); `SetHTTP`, `CheckSpill` |
| `loop_lane.go` | per-lane state: the connection table (`connEntry`, a slice per ring, grown doubling then by half, the `Conn` inside it), request records that outlive a batch, worker slots, response pool |
| `loop_serve.go` | `Feed` and the stream events: decode, map, serve, answer, buffer or stream the body, `Abort`/`ResetStream`, `OnBatchEnd` |
| `loop_request.go` | RFC 9113 §8.3.1's mapping onto `HTTP1RequestValue`, a split Cookie joined, the §8.2.2 fields malformed, the canonical target from `http1.Canonical` (#962) |
| `loop_batch_external_test.go` | one `EndBatch` for a batch serving both versions on a lane, none for an empty one |
| `loop_pending.go` | the worker path (`Worker`, `OnDone`, a slot per Pending stream, an RST_STREAM cancelling), the drain's GOAWAY (`Draining`, `InFlight`), `Pending` |
| `frame.go` | `ParseFrameHeader`, `AppendFrameHeader`, `AppendSettings`, `AppendSettingsAck`, `AppendWindowUpdate`, `AppendRSTStream`, `AppendGoAway`, `AppendPing` |
| `hpack_table.go` | `Decoder` and the dynamic table: a byte ring of entries and a ring of offsets, in memory the caller hands to `Decode` (`TableMem`) |
| `hpack_decode.go` | `Decoder.Decode`: the field representations of RFC 7541 §6, the integer and string readers |
| `hpack_huffman.go` | the string decoder: one nibble at a time through the automaton |
| `hpack_valid.go` | RFC 9113 §8.2.1 on a decoded name and value |
| `hpack_encode.go` | `Encoder`, `AppendHeaders`: never-indexed literals, CONTINUATION splitting |
| `hpack_tables_gen.go` | (`//go:build ignore`) writes the two table files below from RFC 7541's data: `go run hpack_tables_gen.go` |
| `hpack_static_table.go` | the generated static table: bytes and offsets |
| `hpack_huffman_table.go` | the generated Huffman automaton: 256 states of 16 nibble transitions |
| `frame_external_test.go`, `hpack_decode_external_test.go`, `hpack_encode_external_test.go` | the frame layout, every refusal, the ring across its seam, the bomb; the encoder read back by the decoder |
| `hpack_rfc_internal_test.go` | every vector of RFC 7541 Appendix C (C.1–C.6) and the table states they give |
| `hpack_huffman_internal_test.go` | the automaton held to the RFC's code, symbol by symbol, against its own copy of Appendix B |
| `h2c_harness_test.go`, `differential_external_test.go` | the real Go client over Decoder and Encoder (h2c, `Protocols.SetUnencryptedHTTP2`), `FuzzDifferential` |
| `fuzz_internal_test.go` | `FuzzDecode`, `FuzzParseFrameHeader`, `FuzzEncoderDecoderRoundTrip` |
| `conn_external_test.go` | RFC 9113 §5.1's transitions, one by one; 101st stream; Rapid Reset and CONTINUATION floods; PING, SETTINGS and unknown floods; negative and overflowing windows; flow control, coalesced credits, the hold |
| `conn_goclient_external_test.go` | the legal paths through a real Go `http.Transport` (h2c), a stand-in for the loop adapter of #961 over `Lane` |
| `conn_internal_test.go` | `Conn` and `streamState` hold no pointer |
| `perf_fixtures_test.go`, `gen_perf_test.go`, `http2_bench_test.go` | the `allocs: 0` budgets (race off) and the benchmarks `BENCH.md` records |

## Why-this-shape

- **A table with no pointer.** `Decoder` holds only integers; the table's bytes
  are `mem`, handed in by the caller, so one connection's state fits the
  pointer-free slice the loop adapter keeps (ADR 0186 §D7) and an idle
  connection pays no table.
- **An entry never moves, and is read by age in constant time.** A five-byte
  header, the name and the value wrap the ring; a ring of offsets, one per
  slot, gives the entry of a given age. The eviction order is the table's.
- **A field is copied only when it must be.** A plain literal aliases the
  block, a static entry aliases the immutable table, and a Huffman string or a
  table entry goes into `scratch`: a later insert in the same block may
  overwrite a table entry's bytes, and may evict the entry a literal's name
  came from (RFC 7541 §4.4), so a table entry is never aliased.
- **The list ceiling is charged before the copy and before the insert.** A
  field refused by the ceiling costs nothing: no bytes moved, no table touched.
  That is RFC 7541 §7.3's bomb: a block that expands through indexed fields
  costs the ceiling and no more.
- **Malformed is not undecodable.** `FieldMalformed` decodes to the end of the
  block so the table stays in step with the peer, and refuses only the stream;
  a stored malformed field carries its verdict in a flag, so a reference to it
  is refused without a second scan of its bytes.
- **The Huffman table is data, and data is tested.** The automaton is generated
  once and the test reads it back symbol by symbol against the RFC's own codes.
- **Conn is integers.** A connection's windows, settings and the handles of
  what it holds in the lane's arenas (streams, table, hold) — no pointer, no
  slice, so a table of them costs the collector nothing (ADR 0186 §D7), held to
  by a test that walks the type, and an idle connection pays neither a table nor
  a hold.
- **A stream's state is an index into the lane's arena.** Open streams are a
  list threaded through it, newest first, which is sorted by id: a lookup stops at
  the first older id. The arena and its free list grow to a lane's high water
  and no further; a stream never allocates.
- **Feed consumes whole frames and reports events; it does not decode.** An
  event's bytes alias the input (or the lane's scratch for a CONTINUATION-spread
  block), valid until the batch's end, which is exactly what the loop's `OnData`
  contract gives; the adapter decodes each block in order with `Lane.Decode` and
  answers with `Headers` and `Send`. A refused stream's block is still reported
  (`EventDiscard`): HPACK's table must follow the peer whatever happens to the
  stream, RFC 9113 §4.3.
- **HEADERS and its CONTINUATIONs are read as one sequence.** They must be one
  contiguous run anyway, so the walk validates each following frame and either
  finds the whole block or leaves it for the next read: a block trickled one
  byte at a time is walked once (`Conn.scan*`), and a block past
  `4 × MaxHeaderList` bytes or 32 frames is refused before a byte is copied:
  the CONTINUATION flood.
- **The frame budget is per batch and per connection**, charged only to frames
  that ask for an answer or do no work: a batch has at most 1024 of them before
  GOAWAY(ENHANCE_YOUR_CALM). Rapid Reset has its own budget, in resets, refilled
  by streams that finished, so a client that only cancels runs out and one that
  also serves requests does not.
- **Window credits are coalesced by construction.** `Consume` adds to two
  counters and `AppendCredits` turns them into at most one frame per stream and
  one for the connection, in a batch, once a quarter of a window has been
  consumed. A flood of tiny DATA frames cannot make the server flood back.
- **A response is never blocked by a stream's window: it is held.** `Send` writes
  what both windows allow and the rest waits in one buffer per connection, the
  connection's whole held bytes bounded (`holdCap`, the loop's `OutputCap`), one
  range per stream; WINDOW_UPDATE resumes them in `Feed`, in the order the streams
  opened, newest first.
- **Refusal is a return value, never a panic, on any input.** `FuzzDecode` holds
  every slice inside its homes, the ceiling, and a usable table.

## The loop adapter (#961)

- **A connection's version is decided once, by bytes.** The preface against
  `kindSniffing`; anything else goes to the embedded HTTP/1.1 adapter, which
  never knows. A version the group does not serve is 400 and a close.
- **Over TLS too (#964).** On a TLS group (portable backend) the server offers
  h2 then http/1.1 by ALPN, or the Protocols list, and tells the adapter the
  offered list through `SetHTTP`; the preface still decides, the negotiated
  protocol unread, so RFC 9113 §3.3 is not enforced. The sniff holds at most the
  preface's 24 bytes; a peer that stalls inside it is the group's IdleTimeout's.
- **Bodies are buffered on the lane, credited as they are buffered.** A body
  bigger than a stream window therefore arrives; the connection's bodies are
  bounded by `ReceivePerConn` of credited bytes, and past it a stream's credit
  waits for its answer, flow control being the back-pressure. A handler that
  reads as it arrives (`HTTP1BodyReader`) keeps no buffer.
- **A reset stream never reaches the handler** when the reset came in the same
  read (`onHeaders`): the Rapid Reset budget stays the codec's, and the handler
  costs nothing either.
- **A worker's answer is written at once**, in `OnDone`: streams are
  independent, so nothing waits behind it, and the frames may interleave with
  the batch. A reset stream's answer is dropped there and its slot freed.
- **Draining uses `LoopHTTPCodec`.** The loop knows nothing of GOAWAY: the
  server calls `Draining`, the adapter posts itself to each lane, writes
  GOAWAY(NO_ERROR) and closes the connections with no open stream; `InFlight`
  keeps the rest open until their streams end, through `group.loopBusy`.
- **`Honours` Protocols; `SetHTTP` carries it, and the `http2-loop` switch**,
  before `OnStart` — and `CheckSpill` refuses a group whose spill cannot hold
  one frame or field block.
- **`EndBatch` runs once a batch, whichever version served it.** `OnBatchEnd`
  reports HTTP/2's requests to the embedded adapter (`http1.MarkServed`), whose
  `OnBatchEnd` calls the batcher once, before either version's answers are
  written; this adapter then completes its deferred answers and writes them.
  `TestLoopHandler_oneEndBatchForBothVersions` holds it.
- **Counts, once a batch (#966).** The codec counts every RST_STREAM and GOAWAY
  `Feed` writes with an error code by `corehttp2.ErrorType` in the `Lane`
  (`TakeErrors`); the adapter counts its own answers — a field block past
  `MaxHeaderList` is `header_list`, one HPACK refuses `compression`, a malformed
  request `protocol` — and at the batch's end adds the lane's errors and its
  requests to atomics `HTTPCounts` (`corenet.LoopHTTPCounter`) reads; the
  HTTP/1.1 requests are the embedded adapter's. Nothing is counted per request.

## Limits

| Bound | Default | Refusal |
|---|---|---|
| open streams | `DefaultMaxStreams` 100 | RST_STREAM(`REFUSED_STREAM`), table kept in step |
| field block (bytes, frames) | 4 × `MaxHeaderList`, 32 CONTINUATION frames | GOAWAY(`ENHANCE_YOUR_CALM`) |
| control frames a batch, per connection | 1024 | GOAWAY(`ENHANCE_YOUR_CALM`) |
| resets not balanced by finished streams | 2 × `MaxStreams` | GOAWAY(`ENHANCE_YOUR_CALM`) |
| held bytes | the lane's `holdCap` (`OutputCap`) | GOAWAY(`ENHANCE_YOUR_CALM`) |
| frame payload | `DefaultMaxFrame` 16 KiB, at most 16 MiB - 1 | `FRAME_SIZE_ERROR` |
| header list (RFC 9113 §6.5.2's measure) | `DefaultMaxHeaderList` 8 192 | `HeadersTooLarge` |
| fields per block | `corenet.HTTP1MaxFields` 64 | `HeadersTooLarge` |
| dynamic table | `DefaultHeaderTable` 4 KiB, at most 64 KiB - 1 | a size update past it: `CompressionFailed` |
| integer | 5 continuation bytes, under 2^28 | `CompressionFailed` |

## API for what comes next (#963–#967)

`Lane` is per
lane and never shared across lanes. #965's option carries a
`corehttp2.LimitsValue`; `Resolve` is the refusal, with the name in `reason`.

## Do NOT

- Alias a table entry into a request: a later insert of the same block overwrites it.
- Charge a field after copying or inserting it: that is the bomb.
- Treat a `CompressionFailed` block's table as usable: the connection ends.
- Import anything but the standard library, `core/net`, `core/net/http2`, the kernel, `core/observe/trace` and `http1` (the adapter serves both versions) — ADR 0156.

## Verification

```
cd internal && GOWORK=off go test -race ./service/net/http2/
cd internal && GOWORK=off go test -run Perf ./service/net/http2/          # the allocation lane
cd internal && GOWORK=off go test -run '^$' -fuzz FuzzDecode -fuzztime 60s ./service/net/http2/
cd internal && GOWORK=off go test -run '^$' -fuzz FuzzDifferential -fuzztime 60s ./service/net/http2/
cd internal && GOWORK=off go test -run '^$' -fuzz FuzzFeed -fuzztime 60s ./service/net/http2/
```

## Reference

- ADR 0221; ADR 0186 §D6, §D7; ADR 0195 §D3; ADR 0160; ADR 0165.
- RFC 9113 §4, §6, §8.2; RFC 7541.
