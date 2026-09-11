<!-- updated: 2026-09-11T00:00:00Z -->
# internal/service/net/websocket/

## Purpose

The server side of WebSocket, RFC 6455 (ADR 0047): an HTTP request upgraded into
a bidirectional, message-oriented connection over the same socket.

Public façade: `pkg/v1/server/websocket`.

It is written against `net/http`'s own interfaces — `http.ResponseWriter`,
`*http.Request`, `http.ResponseController` — not against the SDK's listener
engine, so it works inside any `http.Handler`. Mounted on the engine
(`Group.HandleHTTP`) it additionally observes the drain signal the adapter
publishes, and the engine hands the socket over instead of closing it — see
§Ownership.

Everything is stdlib. There is no `gorilla/websocket`, no `nhooyr`, and no
`x/net/websocket`: the protocol is a SHA-1 digest and a bit-packed frame header,
and the parts that are hard are the refusals, which no dependency can be trusted
to get right on this SDK's behalf.

## Contents

| File | Surface |
|---|---|
| `websocket.go` | `Conn` — `NewConn`, `Receive`, `Send`, `SendText`, `SendBinary`, `Ping`, `Close`, `CloseWith`, `Done`, `Subprotocol`, `PeerCloseCode`; the frame reader, the message assembler, the write path, the drain watcher and the heartbeat |
| `handshake.go` | `Upgrade` — RFC 6455 §4.2 validation, the origin policy, the hijack probe, the 101 response |
| `options.go` | `Option` — `Subprotocols`, `MaxMessageSize`, `MaxFrameSize`, `PingInterval`, `WithoutPing`, `WriteTimeout`, `AllowOrigins`, `AllowAnyOrigin`; `resolve` and the ADR 0031 clamp/refuse split |

The wire format itself — opcodes, close codes, the frame header, masking, the
UTF-8 rule and the accept-key digest — lives in `internal/core/net/websocket*.go`.
This package owns the *connection*; the core owns the *format*.

## Ownership — the engine defect this domain exposed

`Upgrade` hijacks the response, and a hijacked socket has left the HTTP
lifecycle: `net/http` stops tracking it, and `http.Server.Shutdown` documents
that it neither closes nor waits for one. The SDK engine did neither of those
things. It reported `StateHijacked` through `ConnState`, released the ServeConn
blocked on the connection, and then **closed the socket on the way out** — the
same deferred close that is right for every request/response handler.

The result was that any handler which hijacked got a socket that died
microseconds later, with no error anywhere. It was reachable with nothing but
`net/http` (`TestAHijackedConnectionSurvivesTheEngine` provokes it without a
single WebSocket frame) and nothing in the suite covered it, because no test had
ever hijacked.

The engine now marks such a connection `hijacked` and `release` skips the close.
Two consequences are deliberate and documented:

- **A hijacked connection is not waited for by the drain**, exactly as
  `net/http`'s own `Shutdown` carves out. `Server.State().Active` stops counting
  it too.
- **The drain SIGNAL is therefore the whole mechanism**, not a courtesy. See
  §Drain.

## Why-this-shape

- **`Hijack` is a precondition, and the probe must be side-effect free.**
  Calling `ResponseController.Hijack` to find out whether hijacking is possible
  IS the act, and it cannot be undone — so `canHijack` walks the same unwrap
  chain the controller does, looking for `http.Hijacker`. A middleware that
  wraps the response to count bytes is the normal shape of an HTTP stack, so the
  chain walk is not optional. HTTP/2 is refused by name (`426`), because an
  upgrade replaces the protocol on a socket that HTTP/2 multiplexes; RFC 8441 is
  a different mechanism and is out of scope.
- **The hijacked `bufio.Reader` is used, never the bare socket.** `net/http` may
  have buffered bytes into it while parsing the request. Reading past it on a
  protocol whose every frame is length-prefixed does not lose a few bytes — it
  misparses every frame from then on.
- **A client that pipelines frames onto the handshake is refused.** Buffered
  bytes at that point mean either a broken client or an attempt to slip a second
  request past an intermediary that has not switched protocols yet. Nothing here
  can tell which, and answering would make the guess for it. The refusal closes
  the socket rather than writing a status, because by then the `ResponseWriter`
  is gone and writing to it produces a `net/http` error log and nothing on the
  wire.
- **The inherited deadline is cleared at the upgrade.** The group's
  `ReadTimeout` / `WriteTimeout` are per-REQUEST bounds that `net/http` installs
  on the socket, and there is no request any more. Left in place they would cut
  every connection at one fixed instant, silently, and the operator would see a
  fleet of clients reconnecting on a cycle nobody configured. The write bound is
  replaced by a per-FRAME one.
- **`Receive` returns whole messages.** Fragmentation is the sender's private
  choice of chunk size, not a semantic boundary. Surfacing it would put an
  implementation detail of the peer's writer into every consumer.
- **The returned `Data` aliases the reassembly buffer.** It is valid until the
  next `Receive`, which is what makes a steady-state read allocate nothing. The
  engine's `Conn.Buffer()` carries the same rule for the same reason;
  `TestEchoRoundTrip` would still pass with a copy, so the contract is stated in
  three places rather than inferred.
- **UTF-8 is judged on the REASSEMBLED message.** A four-byte rune may straddle
  a fragment boundary, so a per-frame check would fail perfectly conformant
  connections. `TestUTF8IsJudgedOnTheReassembledMessage` pins both directions —
  the split rune that must survive and the truncated one that must not.
- **The frame ceiling is checked against the ANNOUNCED length**, before a byte
  is read or allocated. A 64-bit length field written by the peer is an
  out-of-memory condition one `make` away. The message ceiling is checked
  against the accumulated total, because a peer under the frame limit can still
  exceed the message limit a thousand small frames at a time.
- **Control frames are exempt from the caller's frame ceiling.** §5.5 already
  caps them at 125 bytes; applying a smaller user bound on top would make `Ping`
  unanswerable to enforce a limit that was never about control frames.
- **The heartbeat is the only liveness check, and it counts frames the handler
  has READ — not pongs, and not frames that merely arrived.** A peer that
  vanishes without closing leaves a socket that is perfectly readable and simply
  never produces another byte — no error, no close frame, nothing. The heartbeat
  sends a Ping and, one interval later, ends the connection if not a single
  frame has been read since. Counting any frame rather than a matching Pong
  means a chatty peer is never probed to death, and a silent one is obliged to
  answer. Counting frames READ is the price: `rx` moves only inside `Receive`,
  so a handler that stops reading looks exactly like a vanished peer — the
  peer's Pong waits unread in the socket buffer, and the connection is ended
  within two intervals (≈60 s at the default) however healthy the peer is.
  Hence the reader contract in §Concurrency and ADR 0047 §D8's 2026-09-11
  amendment. `TestHeartbeatEndsAConnectionNobodyReads` pins that limit, as a
  limit; `TestHeartbeatKeepsAPushConnectionWhileItsReaderRuns` pins the other
  half.
- **A zero ping interval is clamped; a negative one is refused** (ADR 0031).
  "Never" has its own spelling, `WithoutPing()`, and it disables liveness
  detection entirely — which is stated on the option rather than discovered.
- **The origin is checked by default.** The browser's same-origin policy does
  not apply to WebSocket: any page may open a connection and the browser will
  attach the user's cookies to the handshake. An upgrader that accepted every
  origin by default would be a cross-site request forgery primitive with a
  default-on switch. A request with NO `Origin` is allowed — a CLI, a service, a
  Go client — because there is no ambient credential to abuse. The opaque
  `null` origin is refused, since it is equal to nothing including itself.
- **The default origin rule compares the scheme only where it can see it.**
  Host and port are always compared. When TLS ends in this process — `r.TLS`
  is non-nil, which net/http guarantees only for a connection it received as a
  `*tls.Conn`, and the engine hands it exactly that — the Origin must also be
  `https`, because an `http://` page for the same host is the downgrade the
  check exists to notice. When the request arrives in plaintext, as it does
  behind a TLS-terminating proxy, the scheme is invisible and is NOT compared,
  so an `http://` page for the same host is accepted there: **that gap is
  real, and `AllowOrigins` is what closes it**, since it names the scheme.
  Requiring the Origin's scheme to EQUAL the request's would have answered 403
  to every browser behind every proxy; `X-Forwarded-Proto` is not consulted,
  because where no proxy overwrites it the client wrote it. The flip side: a
  proxy that re-encrypts to this server makes `r.TLS` non-nil even when
  browsers reach it over plain http, and such a deployment is refused until
  `AllowOrigins` names its `http://` origin. The truth table is
  `TestDefaultOriginRuleComparesTheSchemeWhereTLSEndsHere`, mutation-checked.
  Before 2026-09-11 the default compared the host alone, while ADR 0047 §D7
  promised it compared the scheme too.
- **The server's subprotocol preference decides.** A client advertises what it
  can speak; choosing among those is the server's call, or a client that listed
  a deprecated dialect first could pin the server to it forever. No overlap is
  not a failure — §4.2.2 makes an omitted `Sec-WebSocket-Protocol` the way to
  say "none agreed".
- **The SDK never fragments what it sends.** Fragmentation exists so a sender
  can begin a message whose length it does not yet know; every message this API
  can express is already in memory. Splitting it would add a failure mode — half
  a message on the wire when the socket dies mid-sequence — in exchange for
  nothing.
- **One Close frame, ever.** §5.5.1 allows exactly one per endpoint, and
  `closeSent` is set before the write so a failed attempt cannot leave the door
  open for a second on a socket that is already gone.

## permessage-deflate is NOT negotiated, and the refusal is enforced twice

The extension is out of scope: it would pull `internal/service/transform` into
this package and roughly double its surface — a compression context per
connection, a sliding-window parameter negotiation, `client_no_context_takeover`
and its three siblings, and a whole second class of decompression-bomb bound to
get right.

The refusal is not a documentation claim:

1. The 101 response carries **no** `Sec-WebSocket-Extensions` header, which
   §4.2.2 defines as the way a server says it uses no extension. Offering the
   extension does not fail the handshake — the connection simply proceeds
   uncompressed, which is what a conforming client expects.
2. `ParseWSFrameHeader` refuses any frame with an **RSV bit set**, which is
   exactly what a deflated frame looks like. A client that compressed anyway
   fails the connection with 1002 rather than handing the application a payload
   nothing can decode.

`TestHandshakeNeverNegotiatesAnExtension` asserts both halves in one test.

## Drain

The connection captures `corenet.DrainSignal(r.Context())` as a **channel** at
the upgrade, and a watcher goroutine turns its close into a Close frame carrying
1001 ("going away", §7.4.1) followed by the socket's close.

The request context is deliberately **not** watched. `net/http` cancels it the
moment the handler returns, and a hijacked socket outlives the handler by
design, so a connection that watched it would end at an instant that says
nothing about the peer. Capturing the channel is what keeps the signal working
afterwards.

Because a hijacked connection is no longer waited for (§Ownership), this signal
is not a nicety: without it a deployment would leave every connected client to
discover a severed socket and guess why.

## Concurrency

**Writes are safe for concurrent use; reads are not.**

Writes are serialised by `wmu` because the heartbeat writes from its own
goroutine while the handler writes from another, and a handler fanning messages
in from several producers is the normal shape. Two interleaved frames are not
two messages, they are one corrupt stream.

**Exactly one goroutine loops on `Receive` for the connection's whole life —
not two, and not zero.** This is a contract a caller must meet, and it is
stated on `Conn`, `Receive`, `Done` and `PingInterval`, and in the façade's
package documentation, because that is where a caller reads.

- **Not two:** the protocol is one ordered frame stream, so two concurrent
  `Receive` calls would each take half of a message. There is no lock that
  would make that correct, so there is none.
- **Not zero:** `Receive` is where the peer's Ping is answered (§5.5.2), where
  its Close is replied to (§5.5.1), and the only place `rx` moves. A connection
  nobody reads answers no Ping, completes no closing handshake, and is ended by
  the heartbeat within two intervals with a live peer on the other end. A
  handler that only pushes therefore still runs the loop, in a goroutine of its
  own, discarding what it reads.

A reader inside `Conn` was considered and refused. It would have to own the
reassembly buffer, so either every message is copied out — an allocation per
message, where `TestSteadyStateReceiveAllocatesNothing` gates zero — or the
reader stops at the first message the handler has not taken, which is exactly
where the Pong behind it stops being read. Basing liveness on matched Pongs
instead was refused too: it reverses §D8, and a Pong still has to be READ to be
matched.

Two goroutines per connection are the `Conn`'s own, both joined by `Close`:

- the **watcher**, which turns the drain signal into a closing handshake;
- the **heartbeat**, which is not started at all when it is disabled.

The reading goroutine above is the CALLER's, and needs no joining: once the
connection ends — `Close` included — its `Receive` returns the terminal error.

`terminate()` (end + close the socket) is separate from `Close()` (terminate,
then join) on purpose: the heartbeat and the watcher call `terminate` when their
own work fails, and calling `Close` there would make a goroutine join itself.

## Error range

None of its own. Every failure is an `internal/core/net` sentinel
(`0.2.11.27` – `0.2.11.33`), wrapped with the offending frame, option or header.
Per ADR 0029 the service layer declares **no** codes.

## Cost

Measured in `BENCH.md` — median of five runs (nine for the fragmentation
family) on an 8-core AMD EPYC 7351P VM, `go1.27.1`. The numbers below are the
ones a reader needs before touching this package; the report has the rest,
including every run behind every median.

| | |
|---|---|
| One 4 KiB binary message received | **3 274 ns**, 0 B, **0 allocs** |
| The same message sent | 232 ns, 0 B, 0 allocs |
| Text instead of binary | **+0.037 ns/byte**, identically on both sides — 4.6 % of a 4 KiB message |
| One additional frame in a fragmented message | ≈115 ns |
| A `Ping` answered between two fragments | +210 ns, of which 140 ns is the Pong §5.5.2 obliges |
| A whole connection: `NewConn`, its watcher goroutine, terminate, join | 2 307 ns, 9 allocs |

**Steady-state `Receive` and `Send` allocate nothing**, and since 2026-09 that
is a test rather than a claim — `websocket_alloc_internal_test.go`, `!race`,
gated by `//internal/service/net/websocket:websocket_test` in
`tools/alloc-lane-targets.txt` (rule 12). Both guards are mutation-checked and
carry the observed failure in their doc comments. The first message on a
connection legitimately costs 4 885 ns and 4 672 B — that is the reassembly
buffer being allocated once, which is exactly what makes every later message
free and why a `Receive` result is valid only until the next call.

### Refusing is cheaper than complying

The question the refusal rows exist to answer: **no MUST-fail in this package is
a denial-of-service lever.**

- Every framing refusal lands in an **880–959 ns** band. Nine of them, detected
  at nine different depths, spanning 8.9 % end to end — so *where* the check
  sits is not what the cost is made of. What it is made of is the typed `errs`
  error (one allocation more than an acceptance) and the §7.1.7 courtesy Close.
- A refusal is **1.6–1.7×** the cost of accepting a 64-byte message and
  **16–18 %** of accepting a 4 KiB one.
- The frame ceiling is the case that matters: refusing a header that announces
  2 MiB and carries nothing costs 959 ns against roughly 1.6 ms to accept what
  it claimed — **0.06 % of the work refused.** That ratio is a direct
  consequence of checking the bound against the ANNOUNCED length before any
  read, and the benchmark enforces it: those scripts carry a header and no
  payload over a socket that reports `io.EOF`, so a check moved after the read
  reports `WSConnClosed` and fails the row instead of publishing a number for
  the wrong path.
- Reaching a refusal at all costs the peer a TCP connect plus the §4.2
  handshake: **262 653 ns** on loopback. A refusal is 0.37 % of that.

### Where the time actually goes — and it is not here

Between **62 % and 89 %** of a received message is `corenet.ApplyWSMask`, the
per-byte XOR §5.1 makes mandatory on every inbound byte. This package's own flat
time is ~13 % of the worst case (256 fragments) and ~2 % of the ordinary one.

`ApplyWSMask` is byte-at-a-time and runs at 1.36 GB/s; the same transform eight
bytes at a time measures 7.4 GB/s — **5.5×**, which would take a 4 KiB receive
from 3 274 ns to roughly 810 ns. It lives in `internal/core/net`, so it is
**recorded and not taken here**; see `BENCH.md` §"The cost that dominates is in
another package".

Two optimisations inside this package were tested and **refused with numbers**:
`bufio.Peek`/`Discard` in `nextHeader` (+1.1 % on realistic shapes, −2.4 % only
at 256 fragments, and it would make header reading depend on `bufio`'s minimum
buffer size), and skipping the per-frame `rx.Add(1)` when the heartbeat is off
(unmeasurable — two of three rows got *slower* with the work removed). Neither
was refused on security grounds, because no candidate that weakened a refusal
was entertained.

## Do NOT

- Read from the raw socket instead of the `bufio.Reader` the hijack returned.
- Call `ResponseController.Hijack` to test whether hijacking is supported — it
  IS the hijack.
- Write to the `ResponseWriter` after the hijack. It produces a `net/http` error
  log and nothing the peer can see.
- Tolerate an unmasked client frame, a reserved bit, a reserved opcode, a
  fragmented control frame or a non-minimal length. Each is a MUST-fail in
  RFC 6455, and a frame stream that keeps going after a disagreement is two
  endpoints reading different messages from the same bytes.
- Send 1004, 1005, 1006 or 1015, or accept them from a peer.
- Allocate from a frame's announced length before the ceiling has been checked
  against it.
- Retain a `Receive` result past the next `Receive` without copying it.
- Call `Receive` from two goroutines — or from none. A connection nobody reads
  is ended by the heartbeat within two intervals; see §Concurrency.
- "Fix" that by starting a reader inside `Conn`, or by basing liveness on
  matched Pongs. The first breaks `Receive`'s aliasing and its zero-allocation
  read; the second reverses ADR 0047 §D8 and still needs a reader to see the
  Pong.
- Make the default origin rule demand that the Origin's scheme equal the
  REQUEST's, or trust `X-Forwarded-Proto` to decide it. The first refuses every
  browser behind a TLS-terminating proxy; the second believes a header the
  client wrote wherever no proxy overwrites it. The gap they would close is
  `AllowOrigins`'s to close.
- Add permessage-deflate without the ADR that decides it. See above.
- Write anything to stdout (ADR 0030). The only output is the connection.
- Make a refusal cheaper by making it conditional, by moving a check off the
  read path, or by checking a bound after the allocation it guards. §Cost
  measures the price of every MUST-fail and it is 0.37 % of what it costs a peer
  to reach one; there is nothing here to reclaim.
- Optimise this package for throughput before reading §Cost. The dominant term
  is `corenet.ApplyWSMask`, in another package, and it is 62–89 % of a received
  message.

## Verification

```
# Primary (Bazel)
bazel test --config=race //internal/service/net/websocket:websocket_test

# Fallback (go test — quick local iteration)
cd internal/service && GOWORK=off go test -race -cover ./net/websocket/...
# expected: coverage ~94%

# The allocation guards. They carry //go:build !race, so the race suite above
# does NOT compile them and this is their only lane (rule 12):
bazel test --config=alloc //internal/service/net/websocket:websocket_test
cd internal/service && GOWORK=off go test -run=TestSteadyState ./net/websocket/

# The numbers in §Cost:
cd internal/service && GOWORK=off go test -run='^$' -bench=. -benchmem \
    -benchtime=1s -count=5 ./net/websocket/
```

The adversarial table (`TestAdversarialFramesFailTheConnection`) is the point of
the suite: every case is bytes assembled by hand, cited to the RFC section it
violates, and asserted to produce the close code the RFC names. A test that
spoke through this package's own writer could only prove the writer and the
reader agree.

## Reference

- ADR 0047 — `docs/adr/0047-sdk-net-websocket.md`
- `BENCH.md` — the full report behind §Cost: the profiles quoted verbatim, the
  two refused optimisations, and the two row-sets that were re-measured rather
  than published
- ADR 0029 (the net domain), ADR 0043 (drain is a signal)
- ADR 0030 (stdout is a protocol channel), ADR 0031 (zero values are never inert)
- Contract layer — `internal/core/net/CLAUDE.md`
- The drain signal's publisher and the hijack hand-over —
  `internal/service/net/server/CLAUDE.md`
- RFC 6455 — the sections each refusal cites are in the test names
