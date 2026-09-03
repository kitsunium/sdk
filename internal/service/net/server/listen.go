// Package server — listener construction.
package server

import (
	"context"
	"crypto/tls"
	stdnet "net"
	"strings"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// streamNetworks are the socket families the stream engine serves.
var streamNetworks = map[string]struct{}{
	"tcp": {}, "tcp4": {}, "tcp6": {}, "unix": {}, "unixpacket": {},
}

// listen binds one address, wrapping it in TLS when the group carries an
// identity. TLS is applied here rather than inside the accept loop so the
// handshake is driven by the connection's own goroutine, which is what keeps a
// slow or hostile peer from stalling the accept path for everyone else.
func listen(ctx context.Context, addr corenet.AddressValue, id corenet.IdentityValue) (ln stdnet.Listener, err error) {
	//: reject an unserved family before touching the OS.
	if _, ok := streamNetworks[addr.Network]; !ok {
		//: refuse an unserved family before touching the OS.
		return nil, errs.Wrap(corenet.UnsupportedNetwork, errs.WrapParams{},
			errs.String("network", addr.Network))
	}
	//: an empty target cannot be bound and would otherwise fail obscurely.
	if strings.TrimSpace(addr.Addr) == "" {
		//: an empty target would otherwise fail obscurely inside the stdlib.
		return nil, errs.Wrap(corenet.InvalidAddress, errs.WrapParams{},
			errs.String("address", addr.String()))
	}
	var cfg stdnet.ListenConfig
	raw, lerr := cfg.Listen(ctx, addr.Network, addr.Addr)
	//: the OS refused the bind — in use, permission, bad interface.
	if lerr != nil {
		//: surface the address that could not be bound.
		return nil, errs.Wrap(corenet.ListenFailed, errs.WrapParams{},
			errs.String("address", addr.String()), errs.String("cause", lerr.Error()))
	}
	//: without an identity the listener stays plaintext.
	if id.IsZero() {
		//: no identity, so the listener stays plaintext.
		return raw, nil
	}
	//: ServerConfig also carries the mutual-TLS setting, so one call covers
	//: both TLS and mTLS and they cannot drift apart.
	return tls.NewListener(raw, id.ServerConfig()), nil
}

// boundListener pairs a live listener with the group that owns it.
type boundListener struct {
	// group is the owning group's name.
	group string
	// addr is the address as requested.
	addr corenet.AddressValue
	// ln is the live listener.
	ln stdnet.Listener
}

// Close releases the listener.
func (b *boundListener) Close() error {
	//: closing is what unblocks the accept loop; there is no other signal that
	//: reliably interrupts a blocking Accept.
	return b.ln.Close()
}
