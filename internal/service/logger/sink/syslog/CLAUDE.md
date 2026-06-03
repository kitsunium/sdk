<!-- updated: 2026-05-18T14:30:00Z -->
# internal/service/logger/sink/syslog/

## Purpose

Terminal `Sink` that ships records to a syslog daemon (journald, rsyslog,
a central collector) via UDP or TCP. Produces a minimal RFC5424 envelope:

```
<PRI>1 - - - - - - <payload>
```

`PRI` = `facility * 8 + severity` (default facility `USER` = 1).
`<payload>` is the pre-encoded line handed in by the upstream encoder —
the sink treats it as opaque bytes. Hostname / app-name / procid / msgid /
structured-data slots are fixed to `-` to keep the producer side
allocation-friendly.

## Contents

| File | Role |
|---|---|
| `syslog_sink.go`        | `syslogSink` + `New` + `NewWithConfig` + `makeFrame` |
| `priority.go`           | `severityFor` / `priorityFor` + RFC5424 severity constants |
| `config.go`             | `Config{Dialer}` — pluggable allowlist-aware `net.Dial` |
| `codes.go`, `errors.go` | sentinels — range 0.3.15.\* |

## Constructors

| Constructor | When |
|---|---|
| `New(network, addr)`               | `addr` is trusted / hard-coded |
| `NewWithConfig(network, addr, cfg)`| `addr` is consumer-controlled; plug an allowlist `Dialer` to defuse SSRF |

## Security

- **Plaintext transport.** Frames go out unencrypted over UDP / TCP. Use
  only on trusted networks (localhost, cluster-local mesh, bastion-only
  subnet). RFC5425 TLS transport is tracked for a future release.
- **SSRF surface (CWE-918).** When `addr` is consumer-controlled, plug a
  `Config.Dialer` enforcing an allowlist — otherwise the sink becomes a
  primitive for hitting internal services (`169.254.169.254`, localhost
  admin ports, cluster DNS). See README.md siblings for example dialer.
- **Log injection.** The upstream `encoder/text` strips `\r`, `\n`, NUL
  from `RecordEvent.Message`, so attacker-influenced content cannot
  inject a second RFC5424 frame at the framing layer.

## Behaviour

- `New` rejects empty `addr` (`AddrEmpty`) and any network other than
  `"udp"` / `"tcp"` (`ProtoInvalid`).
- Dial failures wrap as `DialFailed` (EX_IOERR); the constructor's
  defer-on-error path closes the connection if anything past dial fails.
- `Write` builds a single-allocation frame sized for header + payload;
  write failures wrap as `WriteFailed` (EX_IOERR).
- `Flush` is a no-op (synchronous transport); honours `ctx.Err()`.
- `Close` releases the connection; failures wrap as `CloseFailed`
  (EX_IOERR). Subsequent `Write`s surface "use of closed network
  connection" wrapped as `WriteFailed`.

## Error catalogue — range 0.3.15.\*

| Code      | Sentinel        | Trigger |
|---|---|---|
| 0.3.15.1  | `AddrEmpty`     | `New("…", "")` |
| 0.3.15.2  | `DialFailed`    | `net.Dial` rejected (EX_IOERR) |
| 0.3.15.3  | `WriteFailed`   | `net.Conn.Write` failed (EX_IOERR) |
| 0.3.15.4  | `CloseFailed`   | `net.Conn.Close` failed (EX_IOERR) |
| 0.3.15.5  | `ProtoInvalid`  | network is not `"udp"` or `"tcp"` |
| 0.3.15.6  | `CtxCancelled`  | `Write` / `Flush` saw a cancelled `ctx` |

## Do NOT

- Ship logs over a hostile network without TLS — not supported today.
- Pass a consumer-controlled `addr` to `New`; use `NewWithConfig` with a
  dialer that enforces an allowlist.
- Add structured-data fields without re-thinking the framing budget —
  `frameHeaderHint` (`len(envelopeSuffix)+5` = 19 bytes) sizes the single
  allocation.

## Verification

```
bazel test --config=race //internal/service/logger/sink/syslog:syslog_test
```
