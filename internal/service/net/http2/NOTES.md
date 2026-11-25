# internal/service/net/http2 — notes

Reference moved out of `CLAUDE.md` (ADR 0253), unchanged and under the same section titles.

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
| `loop_drain_external_test.go` | the drain's GOAWAY run in the batch that fed a request leaves the connection to `OnBatchEnd`, which writes the answer, then closes it; an idle one closes at once; the frames' order, SETTINGS first and the GOAWAY behind the answer; a worker's answer returned in a batch that fed its connection joins the batch's output, SETTINGS first, whether the drain runs or not (#1385) |
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
| `conn_internal_test.go` | `Conn`, the adapter's entry, `connRequests` and `streamState` hold no pointer; the entry stays within `entryBudget` (80 bytes, #961) |
| `perf_fixtures_test.go`, `gen_perf_test.go`, `http2_bench_test.go` | the `allocs: 0` budgets (race off) and the benchmarks `BENCH.md` records |

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
  keeps the rest open until their streams end, through `group.loopBusy`. A
  connection the batch fed is left to `OnBatchEnd`, which writes the answers
  the batch gave it, then closes it (`closeIfDone`): closed by the
  announcement, its answered streams no longer in flight, those answers were
  refused (#1385). On such a connection the GOAWAY joins the batch's output
  (`writeGoAway`), and so does a worker's answer that comes back in the
  batch (`answerSlot`): written at once, either went out ahead of the
  SETTINGS the batch queued — RFC 9113 §3.4 makes SETTINGS the first frame —
  and the answer's close refused the rest.
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
