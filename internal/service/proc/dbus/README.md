# dbus (internal/service/proc/dbus)

The SDK's message bus client, on the standard library: who owns a well-known
name on a D-Bus daemon and every change of it — what `proc/service`'s
`Type=dbus` is up on — and a name a Go service owns. Internal service
implementation behind the public `pkg/v1/proc/dbus` facade — consumers import
the facade, not this package.

## API

```go
func NewConn(ctx context.Context, address string) (*Conn, error)
func SystemBusAddress() string
func SessionBusAddress() string

func (c *Conn) UniqueName() string
func (c *Conn) NameOwner(ctx context.Context, name string) (string, error)
func (c *Conn) WatchName(ctx context.Context, name string) (<-chan string, error)
func (c *Conn) RequestName(ctx context.Context, name string) (bool, error)
func (c *Conn) ReleaseName(ctx context.Context, name string) (bool, error)
func (c *Conn) Close() error
```

The `0.3.108.*` codes are `internal/core/proc/dbus`'s (ADR 0160). Linux only:
`NewConn` is `UNSUPPORTED_PLATFORM` elsewhere. See `CLAUDE.md` for the bounds and
ADR 0214's S0e amendment for the spike that chose it.
