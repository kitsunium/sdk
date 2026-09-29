# ipc (internal/service/ipc)

A private socket between processes of one machine. Internal service
implementation behind the public `pkg/v1/ipc` facade — consumers import the
facade, not this package.

## API

```go
func NewListener(cfg *Config) (*Listener, error)
func Dial(ctx context.Context, cfg *Config) (*Conn, error)
func RuntimeDir(app string) string

func (l *Listener) Accept() (*Conn, error)
func (l *Listener) Refused() int64
func (l *Listener) Close() error
```

See `CLAUDE.md` for the two gates (the directory, the peer's credentials) and
ADR 0144 for the decision.
