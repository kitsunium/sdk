# ipc (internal/service/proc/ipc)

A private socket between processes of one machine. Internal service
implementation behind the public `pkg/v1/proc/ipc` facade — consumers import the
facade, not this package.

## API

```go
func NewListener(cfg *Config) (*Listener, error)
func NewDialer(cfg *Config) (*Dialer, error)
func Dial(ctx context.Context, cfg *Config) (*coreipc.Conn, error)
func RuntimeDir(app string) string

func (l *Listener) Accept() (*coreipc.Conn, error)
func (l *Listener) Addr() net.Addr
func (l *Listener) Path() string
func (l *Listener) Refused() int64
func (l *Listener) Close() error

func (d *Dialer) Dial(ctx context.Context) (*coreipc.Conn, error)
```

`*Listener` and `*Dialer` implement the `Listener` and `Dialer` ports of
`internal/core/proc/ipc`, which also holds `PeerValue`, `Conn` and the
`0.3.91.*` codes (ADR 0160). See `CLAUDE.md` for the two gates (the directory,
the peer's credentials) and ADR 0148 for the decision.
