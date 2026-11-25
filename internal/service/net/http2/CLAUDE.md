<!-- updated: 2026-11-10T00:00:00Z -->
# internal/service/net/http2/

The reference half of this file moved, unchanged and under the same section titles, to
`internal/service/net/http2/NOTES.md` (ADR 0253): read the section a change touches.
It holds:
§Contents; §The loop adapter (#961).

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
  a hold. Nor a decoder: the HPACK decoder's bookkeeping lives beside its table
  (`tableArena.decoders`), taken at the first field block, the peer's SETTINGS
  ACK kept in a flag until then; the adapter keeps a connection's open requests
  by that table (`laneState.tableReqs`), a request being opened only after a
  decode. The two counters a block or a batch bounds are narrow (`control` a
  uint16, `scanFrames` a uint8), so `Conn` is 68 bytes and the adapter's entry
  80 (`entryBudget`): with the loop's slot, an idle h2c connection costs at most
  the loop's 256 B of Go heap (#961, `TestLoopHTTP2_idleMemory`).
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
