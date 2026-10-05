package ipc

import (
	"context"

	svcipc "github.com/kitsunium/sdk/internal/service/proc/ipc"
)

// Listen opens the private socket at cfg.Path and returns the engine behind
// the Listener port.
func Listen(cfg Config) (Listener, error) {
	ln, err := svcipc.NewListener(&cfg)
	//: a refused configuration is a nil port, never a nil engine inside one.
	if err != nil {
		return nil, err
	}
	return ln, nil
}

// NewDialer returns the engine behind the Dialer port for the private socket
// at cfg.Path. The configuration is checked here, before anything is touched,
// and copied: editing cfg's slices afterwards changes nothing.
func NewDialer(cfg Config) (Dialer, error) {
	d, err := svcipc.NewDialer(&cfg)
	//: the same rule as Listen: a refusal returns no port at all.
	if err != nil {
		return nil, err
	}
	return d, nil
}

// Dial connects to the private socket at cfg.Path, within ctx and one second.
func Dial(ctx context.Context, cfg Config) (*Conn, error) { return svcipc.Dial(ctx, &cfg) }
