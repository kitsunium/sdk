package server

import (
	"context"
	stdnet "net"
	"syscall"
	"time"

	kbackoff "github.com/kitsunium/sdk/internal/kernel/backoff"
)

// bindReport is what binding one address tells State: whether the socket
// holds its address exclusively, and how long the bind waited for it.
type bindReport struct {
	// exclusive is ListenerStateValue.Exclusive.
	exclusive bool
	// wait is ListenerStateValue.BindWait.
	wait time.Duration
}

// exclusiveApplies reports whether a socket of network bound to address takes
// SO_EXCLUSIVEADDRUSE: a unicast TCP or UDP one, on a platform that has the
// option. A multicast socket shares its port with the group's other members
// by design, and AF_UNIX has no port to take.
func exclusiveApplies(network, address string) bool {
	//: Windows alone has the option.
	if !exclusiveAddrSupported() {
		//: nothing to set.
		return false
	}
	//: the inet families; Control names the resolved one (tcp4, udp6, ...).
	switch network {
	case "tcp", "tcp4", "tcp6", "udp", "udp4", "udp6":
	default:
		//: not an inet socket.
		return false
	}
	host, _, err := stdnet.SplitHostPort(address)
	ip := stdnet.ParseIP(host)
	//: a multicast group's members share the port.
	return err != nil || ip == nil || !ip.IsMulticast()
}

// listenControl is net.ListenConfig.Control for one bind: SO_REUSEPORT when
// the address is sharded, SO_EXCLUSIVEADDRUSE when it applies, both before
// bind; nil when neither can, so the bind is the standard library's own.
func listenControl(sharded bool) func(network, address string, rawConn syscall.RawConn) error {
	//: nothing to set before bind.
	if !sharded && !exclusiveAddrSupported() {
		//: the standard library's bind.
		return nil
	}
	//: run with the descriptor, before bind.
	return func(network, address string, rawConn syscall.RawConn) error {
		//: sharding needs every listener on the address to opt in.
		if sharded {
			//: refused, the shard cannot bind.
			if err := reusePortControl(network, address, rawConn); err != nil {
				//: the setsockopt outcome.
				return err
			}
		}
		//: a unicast inet socket on Windows.
		if !exclusiveApplies(network, address) {
			//: nothing more.
			return nil
		}
		var setErr error
		//: the descriptor, before bind.
		if err := rawConn.Control(func(fd uintptr) { setErr = setExclusiveAddr(fd) }); err != nil {
			//: the descriptor could not be reached.
			return err
		}
		//: a socket that cannot hold its address exclusively is not bound.
		return setErr
	}
}

// exclusiveOf reads SO_EXCLUSIVEADDRUSE back from an adopted socket: what a
// supervisor set is reported, never set and never fatal (a socket that cannot
// be read reports false).
func exclusiveOf(sc syscall.Conn) bool {
	//: a socket with no descriptor to read, or a platform without the option.
	if sc == nil || !exclusiveAddrSupported() {
		//: not exclusive.
		return false
	}
	raw, err := sc.SyscallConn()
	//: unreadable counts as not set.
	if err != nil {
		//: not exclusive.
		return false
	}
	var set bool
	//: read, never written.
	if cerr := raw.Control(func(fd uintptr) {
		v, gerr := exclusiveAddrOf(fd)
		set = gerr == nil && v
	}); cerr != nil {
		//: not exclusive.
		return false
	}
	//: as the supervisor left it.
	return set
}

// rebindDelay is the wait after the attempt-th refused bind: 25 ms, doubling,
// held at 1 s, on the SDK's one published backoff (ADR 0103).
func rebindDelay(attempt int) time.Duration {
	//: no jitter: one process rebinding its own address.
	return kbackoff.Value{BaseDelay: 25 * time.Millisecond, MaxDelay: time.Second, Multiplier: 2}.Delay(attempt)
}

// bindRetrying calls bind until it binds, fails otherwise than with inUse, or
// has been called attempts times, waiting rebindDelay between two calls, and
// returns how long it waited: the bounded rebind of an address a restarted
// server's predecessor still holds. ctx ends the wait early with the last
// refusal.
func bindRetrying[T any](ctx context.Context, attempts int, inUse func(error) bool, bind func() (T, error)) (T, time.Duration, error) {
	start := time.Now()
	//: one call, then one per wait, bounded.
	for attempt := 1; ; attempt++ {
		v, err := bind()
		//: bound, refused for good, or out of attempts.
		if err == nil || attempt >= attempts || !inUse(err) {
			var wait time.Duration
			//: only a bind that waited reports the wait.
			if attempt > 1 {
				wait = time.Since(start)
			}
			//: the last outcome.
			return v, wait, err
		}
		timer := time.NewTimer(rebindDelay(attempt))
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			//: the caller gave up: the refusal stands.
			return v, time.Since(start), err
		}
	}
}

// listenerSocket is ln's descriptor, nil when it has none.
func listenerSocket(ln stdnet.Listener) syscall.Conn {
	sc, _ := ln.(syscall.Conn)
	//: a TCP or Unix listener has one.
	return sc
}

// packetSocket is pc's descriptor, nil when it has none.
func packetSocket(pc stdnet.PacketConn) syscall.Conn {
	sc, _ := pc.(syscall.Conn)
	//: a UDP or Unix datagram socket has one.
	return sc
}
