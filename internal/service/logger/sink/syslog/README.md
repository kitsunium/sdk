# service/logger/sink/syslog

Network sink that ships records to a syslog daemon (journald, rsyslog, a
central collector) via UDP or TCP. Produces a minimal RFC5424 envelope:

```
<PRI>1 - - - - - - <payload>
```

- `PRI` = record level + USER facility.
- `<payload>` is the pre-encoded line handed in by the upstream encoder.

## Constructors

| Constructor | When |
|---|---|
| `New(network, addr)` | Default. `addr` is trusted / hard-coded. |
| `NewWithConfig(network, addr, Config)` | Provide a custom `Dialer` — recommended when `addr` comes from untrusted config. |

## Security

**Plaintext transport.** The current implementation ships frames
unencrypted over UDP or TCP. Use only on trusted networks (localhost,
cluster-local service mesh, bastion-protected subnet). RFC5425 TLS
transport is tracked for v1.1 and is NOT available today — do not ship
logs over a hostile network until TLS lands.

**SSRF surface.** `addr` is passed directly to `net.Dial`. When `addr`
is consumer-controlled (env var, admin API input, customer config),
use `NewWithConfig` with a `Dialer` that enforces an allowlist so an
attacker cannot target internal services (169.254.169.254 AWS IMDS,
localhost admin ports, cluster DNS names). Without the allowlist,
the sink becomes a classic CWE-918 SSRF primitive.

Example hardening dialer:

```go
import (
    "fmt"
    "net"

    "github.com/kitsunium/sdk/internal/service/logger/sink/syslog"
)

func allowlistDialer(network, addr string) (net.Conn, error) {
    host, _, err := net.SplitHostPort(addr)
    if err != nil {
        return nil, err
    }
    switch host {
    case "logs.internal.example.com":
        return net.Dial(network, addr)
    default:
        return nil, fmt.Errorf("syslog: host %q not allowlisted", host)
    }
}

sink, err := syslog.NewWithConfig("udp", operatorAddr, syslog.Config{
    Dialer: allowlistDialer,
})
```

**Record content.** The upstream encoder (`service/logger/encoder/text`)
already strips CR / LF / NUL from `Record.Message`, so a log injection
via newlines in the message body is defused at the framing layer.
Attribute values pass through `strconv.AppendQuote` and are safe.

## Lifecycle

- `New` / `NewWithConfig` dial the destination and return a `Sink`.
- `Write` serialises a frame + issues a single `net.Conn.Write` under
  the sink's mutex. UDP datagrams are inherently atomic; the mutex
  protects the TCP framing path.
- `Flush` is a no-op — no userspace buffering — but returns
  `CodeSyslogCtxCancelled` when the caller's context is already done.
- `Close` releases the underlying connection; subsequent `Write`
  returns `CodeSyslogWriteFailed` via the kernel's "use of closed
  network connection" error.

## Error codes

See ADR 0005 / ADR 0006 — range `0.3.15.*`.

| Code | Sentinel | Trigger |
|---|---|---|
| `0.3.15.1` | `AddrEmpty` | `New("", …)` |
| `0.3.15.2` | `DialFailed` | `net.Dial` rejected |
| `0.3.15.3` | `WriteFailed` | `net.Conn.Write` rejected (EX_IOERR) |
| `0.3.15.4` | `CloseFailed` | `net.Conn.Close` rejected (EX_IOERR) |
| `0.3.15.5` | `ProtoInvalid` | network is not "udp" or "tcp" |
| `0.3.15.6` | `CtxCancelled` | Write or Flush saw a cancelled context |
