<!-- updated: 2026-10-04T11:15:00Z -->
# internal/core/proc/ipc/

## Purpose

The private socket's **contract** (ADR 0148, ADR 0160 §1): who is at the
other end of a connection (`PeerValue`), the connection that carries that
answer (`Conn`), the two ports a private socket is reached through —
`Listener`, the accepting end, and `Dialer`, the connecting end — and the
codes every refusal carries. The engine — a Unix socket in a 0700 directory
whose whole path is audited, `SO_PEERCRED` on Linux, a named pipe with its own
DACL on Windows — is `internal/service/proc/ipc`, at the mirrored path.
Nothing here opens a socket.

It exists because `ipc` was the one service domain whose callers had no port
to hold: the framework's listeners and the telemetry exporter held the engine
itself, so a test of the code around them needed a socket on disk. A caller
that holds a `Listener` or a `Dialer` can be handed a double — `net.Pipe`
connections with the peer the test chooses.

Range `0.3.91.*`. It was allocated to the service package and keeps its value
here: `LL = 3` records the layer that ALLOCATED the range, not the directory its
declaration lives in (ADR 0160 §3). Imports: `context`, `net` and
`internal/kernel/errs`.

**The ports are generated from the design** (ADR 0163): `Listener` and `Dialer`
are declared, with their doc comments, under `ports:` in
`design/proc/ipc.yaml`, and `kit gen` writes them into `design_gen.go`. A port
or its doc comment changes in the design, then `kit gen`, then `make api` —
never in `design_gen.go`, whose header digests `make api-check` verifies. They
moved there from `ipc.go`, content moved and never deleted.

## Surface

| Symbol | Kind | Notes |
|---|---|---|
| `PeerValue` | value | `UID`/`GID`/`PID` as the kernel names them (`-1`/`0` when it does not), `SID` on Windows, `Verified` — false where only the directory admitted the peer |
| `Conn` | value | a `net.Conn` with its `Peer` |
| `Listener` | port, frozen at five | `Accept() (*Conn, error)`, `Addr`, `Close`, `Path`, `Refused` |
| `Dialer` | port, frozen at one | `Dial(ctx) (*Conn, error)` |
| `Code*` | `errs.Code` | `0.3.91.1` – `0.3.91.9` |
| `Misconfigured` … `PathUnsafe` | `*errs.Error` | each var's name is its Reason in SCREAMING_SNAKE form (ADR 0020) |

The engines satisfy the ports by compile-time assertion in the service
(`var _ coreipc.Listener = (*Listener)(nil)`, the same for `Dialer`).
`pkg/v1/proc/ipc` publishes the ports as `Listener` and `Dialer`, the values
as `Peer` and `Conn`, and the codes; `Config` is the engine's own (ADR 0074 —
a single engine's configuration is owned by that engine) and stays in the
service.

## Why the ports have these methods

- **`Listener` keeps every method the engine's handle had**, `Path` and
  `Refused` included, so code that called them through the published
  `*Listener` calls them through the port unchanged. `Refused` is the only
  window on the admission gate: a peer the configuration does not admit is
  closed without a word, and a count is how an operator learns it happened.
- **`Dialer` has no configuration argument.** A dialer is built for one
  endpoint and checks its configuration once, at construction; a port taking
  the configuration per call would hand every double the engine's validation
  to reimplement.
- **Both are frozen** (ADR 0039). A method added to a published interface
  breaks every double a caller wrote; a capability that comes later is a
  sibling interface an endpoint may also implement.

## Error codes

| Code | Sentinel | Exit | Meaning |
|---|---|---|---|
| 0.3.91.1 | `Misconfigured` | 78 | empty or relative path, beyond `sun_path`, a negative UID or GID |
| 0.3.91.2 | `DirectoryUnsafe` | 78 | the socket's directory is a link, not a directory, not ours, or group/world writable |
| 0.3.91.3 | `InUse` | 70 (default) | a live process answers on the path |
| 0.3.91.4 | `ListenFailed` | 70 (default) | the kernel refused the listen |
| 0.3.91.5 | `PeerRefused` | 70 (default) | a kernel-named peer the configuration does not admit |
| 0.3.91.6 | `DialFailed` | 70 (default) | nobody answered |
| 0.3.91.7 | `EndpointForeign` | 78 | the socket file, or the pipe's server, is another account's |
| 0.3.91.8 | `Closed` | 70 (default) | `Accept` after `Close` |
| 0.3.91.9 | `PathUnsafe` | 78 | a component ABOVE the directory another account could steer (ADR 0148, the `pathchain` rule) |

`0.3.91.10` – `0.3.91.255` reserved. No Public string quotes a path: the path
travels in the `path` field. The Private strings name `service/proc/ipc`,
where each condition is detected.

## Do NOT

- Put the engine here — the directory audit, the socket, the pipe, the peer
  credentials. They are mechanisms (ADR 0160 §4) and stay in the service.
- Move `Config` here: it is the one engine's configuration (ADR 0074).
- Add a method to `Listener` or `Dialer`. Add a sibling interface.
- Renumber a code to make its `LL` byte say "core" (ADR 0160 §3), or rename a
  sentinel var — its name is its Reason.

## Verification

```sh
bazel test //internal/core/proc/ipc:ipc_test
# Fallback
cd internal/core && GOWORK=off go test -race ./proc/ipc/...
```

`Test_sentinels` pins each sentinel's code and Reason inside `0.3.91.*`;
`Test_theConnectionPortsTakeADouble` drives code that holds the two ports with
`net.Pipe` doubles, and sees the `CLOSED` and `DIAL_FAILED` verdicts the
engine gives.

## Declarations

`decl_gen.go` is written by kit gen from the design (ADR 0170): the declarations of `PeerValue` and `Conn` — each struct with every field, unexported ones included. Their methods, constructors and helpers stay hand-written, in the files this document names.
