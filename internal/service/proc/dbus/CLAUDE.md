<!-- updated: 2026-11-08T14:30:00Z -->
# internal/service/proc/dbus/

## Purpose

The SDK's **message bus client** (kitsunium/sdk#737, ADR 0214's S0e
amendment, the spike's option (a)): a connection to a D-Bus daemon over a
unix socket, written on the standard library (ADR 0156) — no vendor, no cgo,
no reflection, no process launched. It speaks what a supervisor needs to run
systemd's `Type=dbus` — who owns a well-known name, and every change of it —
and what a supervised Go service needs to be one: owning its name. Its
`*Conn` is `proc/service`'s `BusWatcher` as it is. Contract (the codes):
`internal/core/proc/dbus`. Facade: `pkg/v1/proc/dbus`.

## Shape — why

- **One reader, calls by serial.** `read` is the only goroutine reading the
  socket; a reply goes to the call waiting on its serial (`pending`), a
  `NameOwnerChanged` to the name's watches. A write holds `wmu` for one whole
  message; a write that fails ends the connection, a half-written message
  leaving the stream unreadable.
- **Lookup after AddMatch, ordered by the reader.** `WatchName` registers
  the watch, asks for the name's announcements (`AddMatch`, arg0 the name),
  then looks the owner up (`GetNameOwner`). The lookup's reply is taken in
  the reader (`pending.onReply` → `looked`) before the next message is read:
  an announcement read earlier is older than the lookup and dropped
  (`ready` false), one read after it is newer and queued. No change is lost
  or reordered.
- **A watch never blocks the reader.** Each watch has a queue (guarded by
  `mu`) and one delivery goroutine (`deliver`); past `maxBacklog` (64) the
  newest owners coalesce into the last, the oldest kept — the first loss and
  the final level both survive a reader far behind.
- **Bounded waits.** Every call waits 25 seconds at most (`callTimeout`,
  sd-bus's and libdbus's default reply timeout) or its context's deadline
  when sooner — `CALL_TIMEOUT`; a cancelled context is `CALL_CANCELLED`. The
  authentication's reads and writes carry the same deadline. A late reply is
  dropped. `proc/service` waits for `WatchName` on its actor, so a bus that
  never answers costs a start 50 seconds, never the unit.
- **Bounded reads.** A header's field array past 64 KiB, a byte order that
  is none, a field whose value is not a basic type, a string that is not
  UTF-8 or holds a NUL: `PROTOCOL_VIOLATION`, the connection closed. A body
  past 1 MiB is discarded unread (`bufio.Reader.Discard`), never allocated.
- **No identity in AUTH EXTERNAL.** The client sends `AUTH EXTERNAL`
  alone and answers the bus's `DATA` with an empty one, as sd-bus does: the
  bus takes the user the kernel names as the socket's peer. A uid named
  there is the client's own namespace's — 0 in a sandbox's user namespace
  (S2) — and the bus, which sees the test's or the agent's user, refuses it.
- **The bus driver alone.** Every call goes to `org.freedesktop.DBus`; a
  reply or a `NameOwnerChanged` whose sender is not the driver — a sender
  the daemon itself stamps — is dropped, and so is any method call another
  peer sends this connection.
- **Linux only.** `NewConn` is `UNSUPPORTED_PLATFORM` elsewhere: the BSDs'
  daemons want the credentials byte with `SCM_CREDS`, macOS's bus is
  launchd's, Windows' a TCP address with a nonce file. Everything below
  `NewConn` (`open`) is portable, so the protocol tests run on every OS.

## Contents

| File | What |
|---|---|
| `address.go` | `SystemBusAddress`, `SessionBusAddress` (never a launched bus), `socketOf` — the first `unix:path=`/`unix:abstract=` entry, `%XX` unescaped —, `escapeValue` |
| `auth.go` | `authenticate`: the credentials byte, `AUTH EXTERNAL` naming no identity, the empty `DATA` the bus asks for, `OK`/`REJECTED`, `BEGIN`; lines bounded at 512 bytes |
| `wire.go` | the wire: `encodeCall` (little-endian, strings and a uint32), `readMessage` and its `decoder` (both byte orders, the bounds), `message.strings`/`uint32` |
| `conn.go` | `Conn`, `NewConn`, `open` and `handshake` (within ctx), the calls (`roundTrip`, `call`, `answer`), `NameOwner`, `RequestName` (never queued), `ReleaseName`, `Close`, the reader and its dispatch |
| `watch.go` | `WatchName`, the watch's queue and delivery goroutine, `RemoveMatch` (no reply asked) when its ctx ends |
| `name.go` | `checkName`: a well-known bus name, or `NAME_INVALID` |

## Do NOT

- Launch a bus (`dbus-launch`, autolaunch) or read an X11 property: an
  address names a socket, or nothing.
- Name the client's uid in AUTH EXTERNAL: from a user namespace the bus
  sees another, and answers `REJECTED`.
- Accept a reply or a signal from anyone but the bus driver.
- Hold `mu` while sending on a watch's channel or writing the socket.
- Let a test reach the system bus or the user's session bus: the live tests
  launch their own `dbus-daemon`, its socket and configuration under
  `t.TempDir()`, and skip without one.

## Tests

| File | What |
|---|---|
| `fakebus_internal_test.go` | the fake bus: the other end of a `net.Pipe` playing dbus-daemon's part byte for byte — the authentication, each call read, replies and signals written by an encoder of the test's own, in both byte orders |
| `conn_internal_test.go` | the handshake, a refused or garbled authentication, a silent bus within its deadline, its cancellation or 25 s (`TestHandshakeBounded`, `TestCallBounded` on a synctest clock), the watch's order (an announcement older than the lookup dropped, another sender's or name's ignored), `NameHasNoOwner`, a refused `AddMatch`, `RequestName`/`ReleaseName`/`NameOwner`, a cancelled call, a body past 1 MiB dropped, the protocol violations, the backlog's coalescing, a hang-up |
| `address_internal_test.go` | `socketOf`, the bus addresses (env only), the names, `NewConn`'s refusals (off Linux; a TCP address; no listener under `t.TempDir()`) |
| `live_linux_external_test.go` | against a real `dbus-daemon` the test launches (skips without one): a watch through owners, releases and a disconnection; `RequestName` never queued; a name owned before the watch; `*Conn` asserted a `BusWatcher` |
| `live_userns_linux_external_test.go` | the same bus, the client this test binary re-executed in a new user namespace — uid 0 there, the test's user outside, a sandboxed service's case (S2) —: it connects and owns a name the watcher outside sees; skips where the kernel refuses a user namespace |

```sh
cd internal && go test -race ./service/proc/dbus/
```
