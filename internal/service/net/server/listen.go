// Package server — listener construction.
package server

import (
	"context"
	"crypto/tls"
	stdnet "net"
	"runtime"
	"strings"
	"sync"
	"syscall"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	coreproc "github.com/kitsunium/sdk/internal/core/proc"
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
	//: the platform's answer, decided by build tag.
	return shardsFor(requested, network, reusePortSupported())
}

// shardsFor is resolveShards with the platform's SO_REUSEPORT answer passed
// in, so the branch only Windows takes in production is exercised by the
// suite on every OS — it is where the self-contradicting report lived, and a
// branch one platform alone reaches is a branch one lane alone can check.
func shardsFor(requested int, network string, reusePort bool) (count int, degraded bool, reason string) {
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
	if !reusePort {
		//: an explicit request that cannot be met is a real degradation.
		if requested > 1 {
			//: reported, flag and reason together.
			return 1, true, "SO_REUSEPORT unavailable on this platform; using a single listener"
		}
		//: auto-sizing where nothing can shard disappoints no expectation, the
		//: same answer the unix-socket branch gives. It used to return the
		//: reason anyway with degraded=false, so State contradicted itself on
		//: every auto-sized listener on Windows.
		return 1, false, ""
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
	//: a served family this platform has no socket for, refused by name.
	if unavailable := familyUnavailable(addr); unavailable != nil {
		//: UNSUPPORTED_PLATFORM, before the OS is asked.
		return nil, unavailable
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

// familyUnavailable refuses a family the engine serves but this platform has
// no socket for, before the OS is asked — or returns nil.
//
// The refusal is the SDK's uniform UNSUPPORTED_PLATFORM (ADR 0018 §(a)), not
// LISTEN_FAILED: a failed bind is something an operator can fix — a port in
// use, a permission, a path — and no address would ever make this one bind.
// The family and the platform travel as fields, so the log line says which.
func familyUnavailable(addr corenet.AddressValue) error {
	//: every family this platform can open reaches the kernel as before.
	if !platformLacks(addr.Network) {
		//: nothing to refuse.
		return nil
	}
	//: the one answer every missing mechanic in the SDK gives.
	return errs.Wrap(coreproc.UnsupportedPlatform, errs.WrapParams{},
		errs.String("network", addr.Network), errs.String("goos", runtime.GOOS))
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
	// made creates closed on first use, so a zero boundListener works.
	made sync.Once
	// shut closes closed exactly once, whoever closes the listener first.
	shut sync.Once
	// closed is closed when the listener is: it ends an accept loop's backoff,
	// which is not blocked in Accept and so would not see the closure.
	closed chan struct{}
}

// closedSignal returns the channel closed when the listener is.
func (b *boundListener) closedSignal() <-chan struct{} {
	b.made.Do(func() { b.closed = make(chan struct{}) })
	//: the same channel for every caller.
	return b.closed
}

// Close releases the listener, and tells an accept loop waiting out a backoff.
func (b *boundListener) Close() error {
	b.closedSignal()
	b.shut.Do(func() { close(b.closed) })
	//: closing is what unblocks the accept loop; there is no other signal that
	//: reliably interrupts a blocking Accept.
	return b.ln.Close()
}
