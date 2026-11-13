package server

import (
	"context"
	stdnet "net"
	"net/netip"
	"syscall"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	kbackoff "github.com/kitsunium/sdk/internal/kernel/backoff"
)

// bindReport is what binding one address tells State: whether the socket
// holds its address exclusively, and how long the bind waited for it.
type bindReport struct {
	// exclusive is ListenerStateValue.Exclusive.
	exclusive bool
	// shared is ListenerStateValue.Shared: the socket was bound for another
	// process to join (ShareAddress, #1330).
	shared bool
	// wait is ListenerStateValue.BindWait.
	wait time.Duration
	// backlog is ListenerStateValue.Backlog, the accept queue's depth as
	// applied; backlogReason says why it is not what was asked, "" when it is.
	backlog       int
	backlogReason string
	// receiveBuffer and sendBuffer are ListenerStateValue's, read back after
	// bind.
	receiveBuffer, sendBuffer int
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
	//: no host to judge: a unicast bind as far as we can tell.
	if err != nil {
		//: exclusive.
		return true
	}
	ip, perr := netip.ParseAddr(host)
	//: a multicast group's members share the port; a zone ("%eth0") is part
	//: of what netip parses.
	return perr != nil || !ip.IsMulticast()
}

// rebindAttempts is how many binds addr gets: rebindAttempts on a platform
// that holds an address after its socket closed, but one when this server
// already holds a socket on the same port, of the same transport — another
// of its addresses (":8080" then "127.0.0.1:8080", say) refuses the bind
// for as long as it lives, and waiting it out would only delay the error.
func (s *Server) rebindAttempts(addr corenet.AddressValue) int {
	//: a conflict with ourselves is not waited out.
	if s.holdsPort(addr) {
		//: one.
		return 1
	}
	//: the platform's bound.
	return rebindAttempts
}

// holdsPort reports whether one of the server's bound stream listeners has
// addr's port, on the same transport; a port of zero is held by nobody.
func (s *Server) holdsPort(addr corenet.AddressValue) bool {
	_, port, err := stdnet.SplitHostPort(addr.Addr)
	//: no port, or the kernel's choice.
	if err != nil || port == "0" || port == "" {
		//: nobody's.
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	//: each listener bound so far.
	for _, b := range s.listeners {
		_, held, herr := stdnet.SplitHostPort(b.ln.Addr().String())
		//: the same port, on a stream transport.
		if herr == nil && held == port && b.ln.Addr().Network() == "tcp" {
			//: ours.
			return true
		}
	}
	//: free of us.
	return false
}

// listenControl is net.ListenConfig.Control for one bind: the buffers the
// group asked for, SO_REUSEPORT when the address is sharded, SO_EXCLUSIVEADDRUSE
// when it applies, all before bind; nil when none can, so the bind is the
// standard library's own.
func listenControl(sharded bool, spec socketSpec) func(network, address string, rawConn syscall.RawConn) error {
	//: nothing to set before bind.
	if !sharded && !exclusiveAddrSupported() && spec.receiveBuffer <= 0 && spec.sendBuffer <= 0 && spec.userTimeout <= 0 {
		//: the standard library's bind.
		return nil
	}
	//: run with the descriptor, before bind.
	return func(network, address string, rawConn syscall.RawConn) error {
		//: the options, in order.
		return controlBeforeBind(sharded, spec, network, address, rawConn)
	}
}

// controlBeforeBind sets, on one socket and before bind, the buffers the group
// asked for, SO_REUSEPORT when the address is sharded and SO_EXCLUSIVEADDRUSE
// when it applies.
func controlBeforeBind(sharded bool, spec socketSpec, network, address string, rawConn syscall.RawConn) error {
	//: the buffers the group asked for, before bind.
	if err := bufferControl(spec, rawConn); err != nil {
		//: the kernel refused one.
		return err
	}
	//: sharding needs every listener on the address to opt in.
	if sharded {
		//: refused, the shard cannot bind.
		if err := reusePortControl(network, address, rawConn); err != nil {
			//: the setsockopt outcome.
			return err
		}
	}
	//: TCP_USER_TIMEOUT, inherited by every accepted socket (#971).
	if err := userTimeoutControl(spec, network, rawConn); err != nil {
		//: the kernel refused it.
		return err
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
	//: on the published curve.
	return bindRetryingAfter(ctx, attempts, rebindDelay, inUse, bind)
}

// bindRetryingAfter is bindRetrying waiting delay(attempt) after the
// attempt-th refusal. A caller that gave up is never bound again: when its
// ctx ended as the wait did, both ready at once on a guest that ran the
// goroutine late, the select may take either (#1352), and the refusal stands
// all the same.
func bindRetryingAfter[T any](ctx context.Context, attempts int, delay func(int) time.Duration, inUse func(error) bool, bind func() (T, error)) (T, time.Duration, error) {
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
		//: the caller gave up, whichever the select took: the refusal stands.
		if !rebindWait(ctx, delay(attempt)) {
			//: not bound again.
			return v, time.Since(start), err
		}
	}
}

// rebindWait waits d or until ctx ends, and reports whether the caller still
// wants the bind: false once ctx ended, even when d ran out with it.
func rebindWait(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
	}
	//: the caller's word, read after the wait.
	return ctx.Err() == nil
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
