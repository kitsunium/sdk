// Package server — listener construction.
package server

import (
	"context"
	"crypto/tls"
	stdnet "net"
	"runtime"
	"strings"
	"syscall"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// streamNetworks are the socket families the stream engine serves.
var streamNetworks = map[string]struct{}{
	"tcp": {}, "tcp4": {}, "tcp6": {}, "unix": {}, "unixpacket": {},
}

// shardable reports whether an address can carry several listeners.
//
// A Unix socket cannot: SO_REUSEPORT is an IP-socket option, and a second bind
// on the same path fails outright. Sharding a Unix listener is therefore not a
// degradation to report but a request that never made sense.
func shardable(network string) bool {
	//: only the IP families can carry more than one listener on one address.
	return strings.HasPrefix(network, "tcp") || strings.HasPrefix(network, "udp")
}

// resolveShards returns how many listeners to open on one address, and why it
// may differ from what the caller asked for.
//
// Zero means "auto": one listener per core where the platform can shard, which
// is the point at which accept contention stops being the bottleneck. A count
// above one on a platform or family that cannot shard degrades to a single
// listener — reported, never silent.
func resolveShards(requested int, network string) (count int, degraded bool, reason string) {
	//: a single listener is always achievable, so it can never degrade.
	if requested == 1 {
		//: exactly what was asked for, so nothing to report.
		return 1, false, ""
	}
	//: a Unix socket cannot be shared by several listeners at all. Only an
	//: explicit request is a disappointed expectation; auto-sizing is not.
	if !shardable(network) {
		//: a second bind on a socket path fails outright.
		if requested > 1 {
			//: an explicit request that cannot be met is a real degradation.
			return 1, true, "SO_REUSEPORT applies to IP sockets only; using a single listener"
		}
		//: auto-sizing on a unix socket disappoints no expectation.
		return 1, false, ""
	}
	//: without the socket option, N listeners on one address cannot coexist.
	if !reusePortSupported() {
		//: without the option, N listeners on one address cannot coexist.
		return 1, requested > 1, "SO_REUSEPORT unavailable on this platform; using a single listener"
	}
	//: auto-sizing tracks the cores available to run the accept loops.
	if requested <= 0 {
		//: one accept loop per core, which is where contention stops mattering.
		return runtime.GOMAXPROCS(0), false, ""
	}
	//: the caller's explicit shard count is honoured.
	return requested, false, ""
}

// reusePortControl enables SO_REUSEPORT before the socket is bound.
//
// The option MUST be set before bind, which is precisely what net.ListenConfig's
// Control hook exists for; setting it afterwards has no effect.
// The network and address parameters are part of net.ListenConfig.Control's
// signature. They are read only to keep the intent explicit: the option applies
// identically whatever address is about to be bound, so neither is consulted.
func reusePortControl(network, address string, rawConn syscall.RawConn) error {
	_, _ = network, address
	var setErr error
	//: Control runs the closure with the descriptor before bind.
	if err := rawConn.Control(func(fd uintptr) {
		setErr = setReusePort(fd)
	}); err != nil {
		//: the descriptor could not be reached at all.
		return err
	}
	//: the setsockopt outcome, which decides whether this shard can bind.
	return setErr
}

// listen binds one address, wrapping it in TLS when the group carries an
// identity. TLS is applied here rather than inside the accept loop so the
// handshake is driven by the connection's own goroutine, which is what keeps a
// slow or hostile peer from stalling the accept path for everyone else.
//
// trackCloses hands out sockets that report their own Close (see trackedConn),
// for a group whose ceiling must keep counting a connection a handler took
// over.
func listen(ctx context.Context, addr corenet.AddressValue, id corenet.IdentityValue, sharded, trackCloses bool) (ln stdnet.Listener, err error) {
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
	cfg := stdnet.ListenConfig{}
	//: sharding needs every listener on the address to opt in before binding.
	if sharded {
		cfg.Control = reusePortControl
	}
	raw, lerr := cfg.Listen(ctx, addr.Network, addr.Addr)
	//: the OS refused the bind — in use, permission, bad interface.
	if lerr != nil {
		//: surface the address that could not be bound.
		return nil, errs.Wrap(corenet.ListenFailed, errs.WrapParams{},
			errs.String("address", addr.String()), errs.String("cause", lerr.Error()))
	}
	//: the same layers an adopted socket gets, from the same function.
	return layered(raw, id, trackCloses), nil
}

// layered puts a group's layers over a raw stream listener: the close tracker
// when the group has a ceiling to keep counting, then TLS when it carries an
// identity. It is the ONE place that decides them, for a socket this process
// bound and for one a supervisor handed over alike.
//
// The adopted path used to add the tracker and never TLS, so a group with an
// identity served an inherited socket in plaintext and reported no error —
// the supervisor passes a raw socket, and TLS is this process's layer to add.
func layered(raw stdnet.Listener, id corenet.IdentityValue, trackCloses bool) stdnet.Listener {
	//: the tracker goes UNDER TLS, so net/http still receives the concrete
	//: *tls.Conn it type-asserts on to populate Request.TLS.
	if trackCloses {
		raw = &trackedListener{Listener: raw}
	}
	//: without an identity the listener stays plaintext.
	if id.IsZero() {
		//: no identity, so the listener stays plaintext.
		return raw
	}
	//: ServerConfig also carries the mutual-TLS setting, so one call covers
	//: both TLS and mTLS and they cannot drift apart.
	return tls.NewListener(raw, id.ServerConfig())
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
