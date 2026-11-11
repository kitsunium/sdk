# ADR 0220 — an HTTP/2 connection is frames, HPACK and a state machine on bytes

- **Status**: Accepted
- **Date**: 2026-11-09
- **Deciders**: SDK maintainers (issues #959 and #960, the proposals of the
  continuous audit, lead's decisions where they left a choice), revisable while
  v0 ([ADR 0040](0040-changing-a-published-shape-while-v0.md))
- **Amends**: [ADR 0186](0186-a-plaintext-group-may-run-on-an-event-loop-and-its-http-is-a-profile-that-refuses.md)
  §D6 (HTTP/2 moves into a sibling profile of `http1`) and §Deferred
- **Related**: [ADR 0029](0029-sdk-net-domain.md) D3 (the risk of owning HTTP/2's
  flow control and HPACK, named), [ADR 0160](0160-every-service-has-a-core-and-a-code-keeps-its-value-when-it-moves.md)
  (the values the core's, the wire format the engine's),
  [ADR 0195](0195-an-http-groups-protocols-are-computed-once-and-its-alpn-follows-them.md)
  §D3 (the bounds), [ADR 0165](0165-a-performance-contract-is-the-designs-kit-measures-what-the-compiler-decides-and-a-test-holds-the-rest.md)
  (allocs: 0 in the alloc lane), [ADR 0194](0194-a-loop-lane-offloads-to-its-own-worker-and-an-http1-answer-may-be-pending.md) §D3

## Context

ADR 0186 keeps HTTP/2 out of the loop's profile and defers it. Every HTTP/2
client — gRPC, a browser over TLS, a mesh speaking h2c — is served by `net/http`
on the goroutine engine; the loop would need a codec the standard library does
not export (`hpack` sits under `vendor/`, unimportable), which ADR 0029 D3 named
the risk to take. Like `http1`, what the loop needs is a codec that does no I/O
and refuses what it cannot serve, not a server: a frame layer and HPACK on
`[]byte` (#959), then a connection's streams and windows as a state machine
with no goroutine and no pointer (#960). The adapter onto the loop (#961) and
every other step of the series come later.

## Decision

### D1 — A sibling package, stdlib only, no facade yet

The codec lives in `internal/service/net/http2`, its values and sentinels in
`internal/core/net/http2` (ADR 0160): the frame header, the bounds of ADR 0195
§D3 as one value (`LimitsValue`, resolved by `Resolve`, a zero field its
default), the settings, and the codes `0.3.109.*`. It imports the standard
library and `core/net` for the request and response values it fills, as
`http1` does (ADR 0156). A facade `pkg/v1/net/http2` comes with the adapter it
publishes (#961): a frame parser nothing consumes yet is not a public surface.

### D2 — The frame layer judges what nine bytes can

`ParseFrameHeader(buf, maxFrame)` reads the header, refuses a length past
`maxFrame` (`FRAME_SIZE_ERROR`) and a padded frame whose padding is not shorter
than its payload (`PROTOCOL_ERROR`) — it reads the pad length, so a padded
header needs ten bytes — and allocates nothing. It returns a wire `ErrCode`,
the protocol's vocabulary, never an SDK error: what a stream or the connection
may legally carry is the state machine's (#960). `AppendFrameHeader` and the
`Append` family write every frame the server sends.

### D3 — HPACK is written here, a table with no pointer

The static table and Appendix B's Huffman code are generated once, as static
data (`hpack_tables_gen.go`): an array of bytes the compiler lays out and a
256-state nibble automaton, one table look-up a nibble, no allocation and no
`init` work (ADR 0172). A test decodes every symbol through the automaton
against the RFC's own codes, so a mistake in the generator fails loudly.

`Decoder` is one connection's decoder and holds only integers. The dynamic
table's bytes are memory the caller hands to `Decode` (`TableMem(table)`): a
byte ring of entries — a five-byte header, name, value, wrapped — and a ring of
two-byte offsets, one per slot, so reading an entry is constant-time and an
entry never moves. A connection's state therefore fits a pointer-free slice,
and the table's memory is only taken at the first field block: an idle
connection pays nothing for it.

`Decode` fills a request value's `Fields` in arrival order, pseudo-fields
included (the mapping onto `Method`, `Target` and `Host` is #962's). A plain
literal is aliased from the block, a Huffman string or a table entry copied
into a scratch the caller resets at the batch's end, and a static entry aliased
from the immutable table. A field the header-list ceiling refuses
(`HeadersTooLarge`, RFC 9113 §6.5.2's measure: name, value and 32 per field) is
refused before it is copied and before any insert, so a block that expands
through indexed fields costs the ceiling and no more — RFC 7541 §7.3's bomb; a
block that expands by inserting is bounded by the table's own bound the
same way.

Refusals are two kinds, by whether the table survives:

- `CompressionFailed` (a bad index, a size update out of place or past the
  bound, a truncated integer or string, a Huffman string that breaks RFC 7541
  §5.2) and `HeadersTooLarge` leave the table undefined: the caller ends the
  connection with `GOAWAY(COMPRESSION_ERROR)`, as RFC 9113 §4.3 requires;
- `FieldMalformed` (RFC 9113 §8.2.1: a capital, an invalid byte, a
  connection-specific field) decodes the block to its end, table intact, and
  refuses only the stream, RST_STREAM(PROTOCOL_ERROR). A later reference to a
  stored malformed field is malformed from a flag kept with the entry, with no
  second scan.

`Encoder` writes the response side with static-table names and never-indexed
literals only — no dynamic table, no Huffman — so a peer's HEADER_TABLE_SIZE
needs no honouring beyond zero. `AppendHeaders` re-reads `res.Header`'s lines
as literals with lowercased names (`Connection` and the other
connection-specific fields of RFC 9113 §8.2.2 dropped, `content-length` always
the codec's, from `len(res.Body)`) and splits a block past the peer's
`MAX_FRAME_SIZE` over `CONTINUATION` frames.

### D4 — One connection is a state machine on integers

`Conn` is one connection's protocol state — windows, the peer's settings,
`lastStream`, the handles of what it holds — and holds no pointer, so the
adapter keeps a slice of them per lane (ADR 0186 §D7); a test walks the type.
`Lane` is one lane's arenas: the streams (a pointer-free slice, a free list
threaded through it; a connection's open streams are a list, newest first,
sorted by id so a lookup stops early), the HPACK tables (chunks handed out by
index, taken at a connection's first field block) and the held bytes. `Feed`
consumes whole frames and appends what the connection answers itself —
SETTINGS and its ACK, PING answers, RST_STREAM, GOAWAY, held DATA the peer's
credit releases — and reports the rest as events for the adapter: a field
block to decode (a refused stream's too, so the table follows the peer), body
bytes, a reset, a GOAWAY.

RFC 9113 §5.1's transitions are the table the tests name, transition by
transition. Beyond what the RFC requires, a connection pays for what it
makes the server do: HEADERS and its CONTINUATIONs are read as one sequence,
bounded to `4 × MaxHeaderList` bytes and 32 frames and resumed across reads
(the CONTINUATION flood); control frames are bounded per batch; a Rapid Reset
budget is consumed by resets and refilled by finished streams, so a client that
only cancels (CVE-2023-44487) runs out.

### D5 — Windows credit consumed bytes, once per batch, and a response is held

Receive flow control follows the adapter's `Consume`: `AppendCredits` writes
at most one WINDOW_UPDATE per stream and one for the connection a batch, each
crediting only bytes consumed, once a quarter of a window has been; padding
and discarded bytes are credited at once, nothing will read them. `Send` writes
DATA bounded by both windows and the peer's frame size and holds the rest in
one buffer per connection (the connection's held bytes bounded by the loop's
`OutputCap`, past it GOAWAY(ENHANCE_YOUR_CALM), ADR 0194 §D3's rule), resumed in
`Feed` when WINDOW_UPDATE or a raised `INITIAL_WINDOW_SIZE` (which may make a
window negative, §6.9.2) opens them. A response the peer's window holds never
holds the other streams of the connection. The h2 reading of `Connection: close`
is `GoAway`: GOAWAY(NO_ERROR, lastOpen) and no new stream, the connection closing
once its streams end (`Done`); a response complete before its request's body is
RST_STREAM(NO_ERROR), §8.1.

## Consequences

- New: `internal/core/net/http2` (`FrameValue`, `LimitsValue`, `SettingValue`,
  the frame types and flags, `ErrCode` and the `0.3.109.*` codes: `FRAME_MALFORMED`,
  `HEADERS_TOO_LARGE`, `COMPRESSION_FAILED`, `FIELD_MALFORMED`,
  `LIMITS_REFUSED`), `internal/service/net/http2` (`ParseFrameHeader`, the `Append`
  family, `Decoder`, `Encoder`), allocation budgets on `ParseFrameHeader`,
  `Decoder.Decode` and `Encoder.AppendHeaders` (`design/sdk.yaml`), and the
  state machine (`Conn`, `Lane`, `Event`, `Feed`, `Headers`, `Send`, `Consume`,
  `AppendCredits`, `GoAway`, `Abort`, `ResetStream`) with budgets on
  `Feed` and `AppendCredits`.
- Tests: RFC 7541 Appendix C (C.1–C.6, the tables included), the real Go client
  over the Decoder and Encoder and a differential fuzz of the same, RFC 7541
  §7.3's bomb in `BenchmarkDecode_bomb`; RFC 9113 §5.1's table, the 101st stream,
  Rapid Reset, CONTINUATION, PING and SETTINGS floods, windows driven negative and
  overflowing, the Go client through a stand-in adapter over `Lane`, `FuzzFeed`.

## Deferred

- The adapter on the loop (#961), the mapping onto the request value (#962),
  `pkg/v1/net/http2` with #961, the h2 gate lane (#963), TLS (#964), the bounds as
  an option (#965), counters (#966), RFC 8441's extended CONNECT (#967).

## Amendment 2026-11-10 — the adapter on the loop (#961)

`http2.HandleLoop(h, p)` serves HTTP/2 and HTTP/1.1 on one plaintext loop group,
by wrapping (the open question of #961 settled for this form, ADR 0187 keeping
the handler's type as the choice of engine): a connection whose first bytes are
the 24-byte client preface is served by this package's `Lane` on the lane's
thread, anything else by an embedded `http1.HandleLoop(h, p)`, so one chain
judges both; `Protocols` names the versions a group serves (h2c by prior
knowledge, `Upgrade: h2c` not served), honoured through a new
`corenet.LoopHTTPCodec` the server hands the group's settings at Start, and which
also receives the drain's announcement. `KITSUNIUM_NET_DISABLE=http2-loop`
(class A-code, ADR 0189) refuses the preface with 400 and closes, State's row
reading `disabled`. Answers are written per stream on the lane's thread,
Pending ones when the worker's job comes back; a client that resets a pending
stream sees no frame for it, the slot freed by the stale path, nothing counted
as an error. The drain sends GOAWAY(NO_ERROR) from a Post to every lane, and
`InFlight` keeps a connection open until its streams have been answered.
Bodies are buffered on the lane with their stream credited as they arrive, the
connection window holding back what passes `ReceivePerConn`, except for a handler
that reads as it arrives. RFC 9113 §8.3.1's pseudo-fields map onto `Method`,
`Target`, `Host` (`:authority` against a `Host` field, `RefusalTargetHost` when
they differ), a split `Cookie` is joined (§8.2.3), the connection-specific fields
of §8.2.2 are malformed, and the canonical target goes through the HTTP/1.1
parser over a synthesised request line until #962 exports `Canonical`.

An idle h2c connection costs about what an idle HTTP/1.1 one does in the same
harness (TestLoopHTTP2_idleMemory: 305–316 B against 277–328 B); the 256 B of
`loop/CLAUDE.md` is the loop's own, before any adapter, so an adapter cannot be
held to it. Deferred: `Canonical` (#962), the gate lane (#963), TLS (#964),
bounds as an option (#965), counters (#966), extended CONNECT (#967).

## Amendment 2026-11-10 — h2c on a Plaintext HTTP group (#861)

`net/http`'s cleartext HTTP/2 is served by prior knowledge on a `HandleHTTP`
group given `Plaintext()` with `Protocols("h2")` or `("h2", "http/1.1")`:
`http.Protocols.SetUnencryptedHTTP2`, resolving ADR 0195's deferred item (the
Upgrade: h2c form stays unserved; a request carrying it is answered over
HTTP/1.1), State's row saying `H2C`. A group without TLS and without Plaintext
keeps being refused, by PlaintextRequired (ADR 0197).

## Amendment 2026-11-10 — one canonical target for both versions (#962)

`http1.Canonical` is exported, `HTTP1RequestValue` gains `Scheme`, `Proto` and
`Stream` (which `Parse` clears, `Proto` then 1, as `HTTP1AccessValue` gains
`Proto` and `Stream`), and the mapping an HTTP/2 codec applies is written on the
value: `Method` from `:method`, `Target` from `:path` (origin-form or `*`, an
absolute-form `:path` `RefusalTargetForm`), `Host` from `:authority`, else the
Host field, both different `RefusalTargetHost`; the regular fields in `Fields`.
So two readers of one target (ADR 0191 §D3) stay one, on HTTP/2 too. The
adapter also fills `Conn` and `ID` (a counter of its own under the lane, one
bit apart from an HTTP/1.1 request's), and answers the chain's `CORS`,
`RetryAfter` and `RequestID` as fields; a `File`, `Stream` or `Upgrade` answer
has no HTTP/2 form on the loop yet and is a 500 (its file closed).

## Amendment 2026-11-10 — HTTP/2's bounds as a group option (#965)

`server.HTTP2(corehttp2.LimitsValue)` (kind `OptionHTTP2`, 30) resolves ADR 0195's
deferral of the bounds: an HTTP group's `http.HTTP2Config` and the loop adapter's
`Lane` read the same resolved value, a zero field its default, so the zero value
serves ADR 0195 §D3's numbers and State's `ListenerStateValue.HTTP2` says so for
every HTTP listener. A bound out of range fails Start with
`corehttp2.LimitsRefused`, its name in `reason` (the issue's `HTTP2Refused`
`0.2.11.70` is not created: the core's sentinel already says it); a datagram
group refuses the option with PacketOptionUnsupported, a loop group whose handler
does not honour it with LoopOptionUnsupported, any other handler with
ProtocolsRefused. `MaxHeaderList` is the loop adapter's; net/http keeps reading
the group's MaxHeaderBytes for it. Measured: 32 MiB uploaded through 100 ms of
latency takes 13.2 s on the default window and 1.1 s with a 4 MiB stream and
16 MiB connection window, under -race.

## Amendment 2026-11-10 — one EndBatch a batch for both versions

A lane whose batch served an HTTP/1.1 and an HTTP/2 request called the handler's
`HTTP1Batcher.EndBatch` twice — the embedded adapter's call, then this adapter's
after the HTTP/1.1 answers were written —, against the port's contract (once per
iteration that served, before any answer is written): a handler committing one
transaction a batch committed an empty second one. `http1.LoopHandler.MarkServed`
lets the HTTP/2 adapter report its requests to the embedded one, which calls the
batcher once for both versions.

## Amendment 2026-11-10 — requests per version and HTTP/2's errors in State (#966)

`ListenerStateValue` gains `HTTP1Requests`, `HTTP2Requests` and `HTTP2Errors`, a
row per `corehttp2.ErrorType` counted: one type per RFC 9113 code past NO_ERROR,
numbered as the code, and `header_list`, `ping_timeout` and `other`, a fixed
vocabulary a peer cannot grow (`ErrorTypeOf` maps a code). On net/http's engine
the group's handler counts each request by `ProtoMajor`, one atomic add and no
allocation (`BenchmarkHTTP_Adapter` reads 65 allocs/op with and without it), and
`HTTP2Config.CountError` classifies net/http's names (`conn_`/`stream_` and the
code's name, the frame layer's `frame_` names by the code their parser answers,
`conn_close_lost_ping`). net/http counts neither a header list past
`MaxHeaderBytes`, answered 431 by itself, nor a frame past its frame bound: those
are not counted on that engine. On the loop engine the codec counts each
RST_STREAM and GOAWAY `Feed` writes with an error code (`Lane.TakeErrors`), the
adapter what it answers itself — `header_list` for a field block past
`MaxHeaderList` —, and both adapters add their requests once a batch; a new port,
`corenet.LoopHTTPCounter`, carries the numbers to State.

## Amendment 2026-11-10 — h2 beside http/1.1 on a TLS codec group (#964)

Amends [ADR 0198](0198-the-http1-codec-takes-tls-on-the-portable-backend.md) §D1
(HTTP/1.1 alone by ALPN) and §D4 for the h2 adapter, and ADR 0195 §D2's sentence
on loop groups. A TLS loop group whose handler is `http2.HandleLoop` offers ALPN
h2 then http/1.1 — or its Protocols list, which may now name h2 — intersected
with the identity's NextProtos by `offered`, as an HTTP group's; the http1
adapter still offers http/1.1 alone and refuses h2 with ProtocolsRefused. The
adapter is told the offered list as what it serves, so a version the handshake
cannot negotiate is not served by the preface either. Nothing of ADR 0198's
D-N3 moves: the handshake on its goroutine, the portable backend alone (a native
backend refused with LoopTLSUnsupported naming Loop). The version is still chosen
by the preface, at most 24 bytes held in the spill, a stalled one closed by the
group's IdleTimeout; the negotiated protocol is not consulted, so RFC 9113 §3.3
is not enforced: a client that negotiated h2 and speaks HTTP/1.1 is served
HTTP/1.1 when the list holds it, 400 and closed otherwise. Protocols("h2") does
not fail the handshake of a client offering http/1.1 alone: crypto/tls lets such
a client reach an h2-only server with no protocol (Go issue 46310), as on an HTTP
group, and its request is answered 400. `ListenerStateValue.ALPN` reports the
list offered, on both engines.

## Amendment 2026-11-10 — WebSocket over HTTP/2 on net/http's engine (#967)

Amends [ADR 0047](0047-sdk-net-websocket.md)'s scope (HTTP/1.1 alone) and ADR 0195
§D2's remedy (Protocols("http/1.1") for a group serving WebSocket). `websocket.Upgrade`
accepts RFC 8441's extended CONNECT on an HTTP/2 request whose `:protocol` is
exactly `websocket`: the version and origin checks of RFC 6455 §4.2 kept, no key
nor accept (RFC 8441 §5), 200 and never 101, the stream switched to full duplex and
the connection built over it — a `transport` beside the hijacked socket, no
exported symbol changing (ADR 0039). Client masking stays mandatory; the drain's
1001 reaches the stream through the same signal, and the stream ends when the
handler returns, so the handler waits for `Done`. A CONNECT naming another
protocol opens nothing (405), an HTTP/2 GET is still 426. net/http announces
extended CONNECT only under `GODEBUG=http2xconnect=1` (Go issue 71128), which a
consumer's main package sets with `//go:debug`; the tests run in a child with it.
The loop's HTTP/2 adapter still refuses CONNECT and does not announce
SETTINGS_ENABLE_CONNECT_PROTOCOL. No BENCH.md row for a message over HTTP/2 yet.

## Amendment 2026-11-10 — the h2 lane of the paired gate (#963)

[ADR 0190](0190-a-merge-gate-judges-the-ratio-of-two-engines-measured-side-by-side.md)'s
paired gate gains L1h2: L1's request over h2c by prior knowledge, 1 000 connections,
eight streams in flight on each, the client writing pre-encoded frames and counting
END_STREAM so its cost stays flat. Engines `goroutine-h2` (net/http's h2c, #861) and
`loop-h2` (`http2.HandleLoop` on `LoopsAuto`); rows P9 (goroutine-h2 → loop-h2, P6's
0.70), cal-L1h2 (within 1.05) and h1-h2 (loop → loop-h2, reported). All three are
inactive: they are measured on the GitHub runner alone, and turn on once cal-L1h2 has
held on three consecutive runs. The workflow's `^BenchmarkLoad` already runs the
pairs; whether ten rounds still fit its 30 minutes is the runner's to say.
