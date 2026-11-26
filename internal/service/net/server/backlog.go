package server

import (
	stdnet "net"
	"slices"
	"strconv"
)

// maxListenerBacklog is the ceiling the standard library puts on the backlog it
// asks for by default: the kernel's own limits are 16 bits wide.
const maxListenerBacklog int = 1<<16 - 1

// defaultBacklog is the backlog a listener has when the group asked for none:
// the host's somaxconn, as the standard library reads it once and caps it.
// Zero when the platform does not say.
func defaultBacklog() int {
	return min(hostSomaxconn(), maxListenerBacklog)
}

// applyBacklog gives a bound listener the listen backlog the group asked for
// (#978) and says what it ended up with. The kernel clamps a backlog to the
// host's somaxconn without a word, so the value reported is the clamp, with the
// reason the caller's figure was not the one applied: Degraded in State, never
// silently. A request of zero keeps the standard library's backlog and reports
// the host's. listen(2) called again on a listening socket sets its backlog
// anew on every platform that has the call, which is how the stdlib's own
// listener is resized without creating the socket here.
func applyBacklog(ln stdnet.Listener, requested int) (effective int, reason string) {
	//: nothing asked for: the standard library's backlog stands.
	if requested <= 0 {
		return defaultBacklog(), ""
	}
	//: the platform refused, or has no such call.
	if why := setBacklog(ln, requested); why != "" {
		return defaultBacklog(), "the listen backlog could not be set on this socket (" + why + "); the host's applies"
	}
	host := hostSomaxconn()
	//: asked for more than the host allows: the kernel clamped it.
	if host > 0 && requested > host {
		return host, "Backlog " + strconv.Itoa(requested) + " is above the host's somaxconn " + strconv.Itoa(host) + "; the kernel clamps it to " + strconv.Itoa(host)
	}
	//: as asked.
	return requested, ""
}

// joinReason is a degradation reason followed by another, when both exist.
func joinReason(reason, more string) string {
	//: nothing to add, or nothing yet to add it to.
	if more == "" || reason == "" {
		return reason + more
	}
	return reason + "; " + more
}

// widenBacklogs raises, as a drain begins, the listen backlog of every socket
// a group sized with Backlog to the host's somaxconn (#978). Backlog bounds
// what waits for a group that serves; a draining group owes service to what
// is already on its way. A peer the full queue refused retries — its SYN on
// its own timer, 1, 3 and 7 s after the first —, and a final ACK a full queue
// dropped is sent again with the peer's data: into a queue of the group's
// depth, a cohort of them overflows it again whenever the loop is slow to
// take, and a peer dropped at every retry of the grace meets the closed
// listener. Widened, the queue holds them all, and the drain's take serves
// them. A socket a group did not size, an adopted one, and a platform with no
// second listen(2) keep their queue; State reports the depth the group asked.
func (s *Server) widenBacklogs() {
	wide := defaultBacklog()
	//: the platform says no somaxconn: nothing to widen to.
	if wide <= 0 {
		return
	}
	s.mu.RLock()
	listeners := slices.Clone(s.listeners)
	s.mu.RUnlock()
	//: each socket a group sized.
	for _, b := range listeners {
		//: sized by no Backlog.
		if b.sized == nil {
			continue
		}
		//: a refusal keeps the queue's depth: the drain serves what it holds.
		setBacklog(b.sized, wide)
	}
}
