<!-- updated: 2026-10-03T12:00:00Z -->
# internal/service/net/websocket/

The reference half of this file moved, unchanged and under the same section titles, to
`internal/service/net/websocket/NOTES.md` (ADR 0253): read the section a change touches.
It holds:
§On a loop group (#859, ADR 0186 amended); §Contents; §Ownership — the engine defect this domain exposed; §Why-this-shape; §The codec — why it is shaped the way it is; §permessage-deflate is NOT negotiated, and the refusal is enforced twice; §Drain; §Cost; §WebSocket over HTTP/2 (RFC 8441, #967).

## Purpose

The server side of WebSocket, RFC 6455 (ADR 0047): an HTTP request upgraded into
a bidirectional, message-oriented connection over the same socket.

Public façade: `pkg/v1/net/websocket`.

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
- the **heartbeat**, which is not started at all when it is disabled. It is a
  `worker.Every` loop whose tick, `heartbeat`, keeps its two-field state on the
  `Conn` (written by that goroutine only); it ticks on the connection's clock
  (`clock.System`; only `Test_Conn_heartbeatOnInjectedClock` hands it a
  `ManualClock`, through the unexported option field — there is no public clock
  option) and ends on `done` through `worker.WithDone`, so a terminated
  connection stops it at once.

The reading goroutine above is the CALLER's, and needs no joining: once the
connection ends — `Close` included — its `Receive` returns the terminal error.

`terminate()` (end + close the socket) is separate from `Close()` (terminate,
then join) on purpose: the heartbeat and the watcher call `terminate` when their
own work fails, and calling `Close` there would make a goroutine join itself.

## Error range

None of its own. Every failure is an `internal/core/net` sentinel
(`0.2.11.27` – `0.2.11.33`), wrapped with the offending frame, option or header.
Per ADR 0029 the service layer declares **no** codes.

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
- Optimise this package for throughput before reading §Cost and `BENCH.md`
  §The wire format. The per-byte terms are `ApplyMask` and `ValidateText`, and
  both are already at the memory hierarchy rather than the CPU.
- Reach for `unsafe`, assembly or a build-tagged per-architecture file to make
  `ApplyMask` faster. It is already 16× the byte-at-a-time form in pure
  stdlib, and every one of those three costs the single-implementation
  property ADR 0018 requires.
- Read the payload with one `binary` byte order and build the key word with
  another, or with hand-written shifts that assume a memory layout. One order,
  used for both, is the entire endianness argument.
- Delete `applyMaskReference` from `frame_mask_external_test.go`, or
  "simplify" it to call `ApplyMask`. It is the RFC §5.3 transform transcribed
  and the oracle every masking test is judged against; an oracle that calls
  the implementation proves the implementation equals itself.
- Add an RSV field to `FrameHeaderValue`, or tolerate a set reserved bit. It
  is how permessage-deflate is refused where the wire can verify it.
- Treat `crypto/sha1` here as a security primitive, or "upgrade" it. RFC 6455
  §1.3 fixes the algorithm; changing it produces a server that talks to
  nothing.

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
