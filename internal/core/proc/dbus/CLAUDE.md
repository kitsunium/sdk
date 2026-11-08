<!-- updated: 2026-11-08T14:00:00Z -->
# internal/core/proc/dbus/

## Purpose

The **contract** of the SDK's message bus client (kitsunium/sdk#737, ADR
0214's S0e amendment): the codes its refusals carry. The engine — a
connection to a D-Bus daemon over a unix socket, AUTH EXTERNAL, the bus
driver's calls and `NameOwnerChanged`, on the standard library — is
`internal/service/proc/dbus`, at the mirrored path (ADR 0160). Nothing here
dials a socket. The port a supervisor watches a name through is
`proc/service`'s `BusWatcher`, which the engine's `*Conn` satisfies as it is.

Range `0.3.108.*` (`design/sdk.yaml` `codes.ranges`; reserved for #737).
Imports: `internal/kernel/errs`.

**Generated from the design** (ADR 0163): the codes and sentinels in
`gen_codes.go`, the pins in `gen_api_test.go`, the package comment in
`doc.go`, all from `design/proc/dbus.yaml`. Change the design, then
`kit gen`, then `make api` — never the generated files.

## Error codes (range `0.3.108.*`)

| Code | Sentinel | Reason |
|---|---|---|
| `0.3.108.1` | `AddressInvalid` | `ADDRESS_INVALID` — an address that does not parse, or names no `unix:path=` / `unix:abstract=` entry (tcp, nonce-tcp, unixexec, launchd and autolaunch are never tried); exit 78 |
| `0.3.108.2` | `BusUnreachable` | `BUS_UNREACHABLE` — nobody listens on the socket, or the bus hung up during the handshake; exit 69 |
| `0.3.108.3` | `AuthRefused` | `AUTH_REFUSED` — the bus answered `REJECTED` to AUTH EXTERNAL; the `mechanisms` field lists its offer; exit 77 |
| `0.3.108.4` | `ProtocolViolation` | `PROTOCOL_VIOLATION` — an authentication line, a header or a reply body the specification forbids; the connection is closed; exit 76 |
| `0.3.108.5` | `CallFailed` | `CALL_FAILED` — the bus answered a call with an error message; `method` and `error` fields |
| `0.3.108.6` | `CallCancelled` | `CALL_CANCELLED` — the caller's context was cancelled first; the context's error is in the chain |
| `0.3.108.7` | `ConnClosed` | `CONN_CLOSED` — a closed connection, or one the bus hung up; exit 69 |
| `0.3.108.8` | `NameInvalid` | `NAME_INVALID` — not a well-known bus name (a unique `:1.42` is not one); exit 64 |
| `0.3.108.9` | `CallTimeout` | `CALL_TIMEOUT` — no reply within the context's deadline (in the chain) or 25 seconds, sd-bus's default reply timeout; the authentication bounded the same; exit 75 |

Off Linux `NewConn` is `internal/core/proc`'s `UNSUPPORTED_PLATFORM`: no code of
this range says it.

## Do NOT

- Change a code's value: a caller branches on it.
- Put the wire format or a port here: the wire is the engine's, the port
  `proc/service`'s.
