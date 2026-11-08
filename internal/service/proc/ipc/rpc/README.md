# rpc (internal/service/proc/ipc/rpc)

A framed, bounded, codec-typed call between two processes of one machine over
the private socket. Internal service implementation behind the public
`pkg/v1/proc/ipc/rpc` facade — consumers import the facade, not this package.

## API

```go
func NewServer(cfg ServerConfig, r corerpc.Router) (*Server, error)
func NewClient(ctx context.Context, d coreipc.Dialer, cfg ClientConfig) (*Client, error)

func (s *Server) Serve(ln coreipc.Listener) error
func (s *Server) ServeConn(ctx context.Context, c *coreipc.Conn) error
func (s *Server) InFlight() int
func (s *Server) Close() error

func (c *Client) Call(ctx context.Context, method string, in, out any) error
func (c *Client) Stream(ctx context.Context, method string, in any) (corerpc.Stream, error)
func (c *Client) Ping(ctx context.Context) error
func (c *Client) Close() error

type HandlerFunc func(ctx context.Context, req corerpc.Request) (out any, err error)
type Methods map[string]corerpc.Handler
```

The ports (`Handler`, `Router`, `Request`, `Stream`) and the `0.3.106.*` codes
are `internal/core/proc/ipc/rpc`'s (ADR 0160). See `CLAUDE.md` for the bounds
and ADR 0218 for the wire.
