<!-- updated: 2026-11-03T15:00:00Z -->
# internal/service/observe/logger/internal/datagram/

## Purpose

The **batched datagram send both datagram writers share** (#807): the udp
network writer (`writer/nettransport`, its udp seam) and journald
(`writer/journald`, `WriteBatch`). `NewBatchSender(conn)` over a CONNECTED
socket; `Send(payloads)` sends each payload as its own datagram, in order, and
reports how many, from the first, left. On Linux a conn exposing its descriptor
(`syscall.Conn`: `*net.UDPConn`, `*net.UnixConn`) is written with one
`sendmmsg(2)` per `MaxBatch` (64) datagrams under the runtime's poller; any
other conn — a test double, a wrapper — and every other platform take one
`Write` per datagram. No error code: the sinks wrap the raw cause under
`NET_TRANSPORT_WRITE_FAILED` / `JOURNALD_WRITE_FAILED`.

## Contents

| File | Surface |
|---|---|
| `doc.go` | the package comment — kit writes it from the design (`design/observe/logger.yaml`, ADR 0167) |
| `datagram.go` | `MaxBatch`, `BatchSender` (its `batch` func, the platform's, nil off Linux), `NewBatchSender`, `Send`, `sendEach` (the portable loop) |
| `datagram_linux.go` | `newBatch` / `newMmsgBatch`, `mmsgBatch`: the headers and iovecs allocated once, `send` (`MaxBatch` at a time, resumed after a short count), `sendChunk` (one header and one iovec per datagram over the caller's bytes, `runtime.KeepAlive`, the iovecs cleared after), `writePending` (the poller callback, a method value bound once — no closure per call), `sendmmsg`, `setKernelLen`, and `struct mmsghdr`'s layout, its stride asserted as constants — the same assertions `internal/service/net/server` holds for its own copy |
| `datagram_other.go` | `newBatch` returns nil off Linux: darwin's and the BSDs' `sendmsg_x` is not a stable interface |
| `sysnum_linux_{amd64,386,other}.go` | `sendmmsg(2)`'s number: 307 and 345 where the standard library's `syscall` lacks it, `syscall.SYS_SENDMMSG` elsewhere |
| `datagram_external_test.go` | 73 payloads to a connected udp peer arrive as 73 datagrams in order; the unixgram path journald takes; a conn with no descriptor written once per payload, a refusal ending the batch; a closed socket's error |
| `datagram_linux_internal_test.go` | one `sendmmsg(2)` per `MaxBatch`, a short count resumed from the first datagram not sent |
| `datagram_bench_test.go` / `BENCH.md` | 16 records through `Send` against 16 `Write`s |

## Why-this-shape

- **One datagram per payload, never a gathered one.** The udp writer's and
  journald's framing rest on the datagram boundary: a udp collector parses one
  record per datagram, journald one entry. `net.Buffers.WriteTo` — what tcp
  uses — would gather a backlog into ONE datagram on a datagram socket.
- **Not shared with `internal/service/net/server`.** The issue named the net
  domain's `sendmmsg` helper; it is that domain's — addressed replies, UDP GSO
  and its EINVAL/EIO fallbacks, behind `net/server`'s own types — and a logger
  sink importing the server engine would cross two domains for forty lines.
  What is shared is the method: the layout, its constant assertions and the
  syscall numbers, cited from there.
- **Connected sockets only.** No datagram names a peer, so `msg_name` stays
  nil; both writers dial before they send.
- **Not safe for concurrent use.** One scratch per sender; the sink's own
  mutex already serialises every send, as each logger sink does.

## Do NOT

- Gather payloads into one datagram, or split one across two.
- Add an error code here: the cause goes back raw, and the sink's code is the
  one an operator knows.
- Import `golang.org/x/sys` for `Mmsghdr` (ADR 0016 / ADR 0018).

## Verification

```
cd internal && go test -race ./service/observe/logger/internal/datagram/
cd internal && GOOS=windows go vet ./service/observe/logger/internal/datagram/
```
