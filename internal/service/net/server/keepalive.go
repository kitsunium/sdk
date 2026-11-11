package server

import (
	stdnet "net"
	"slices"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// keepAliveProbes is the unanswered probes a keepalive socket tolerates, the
// standard library's own.
const keepAliveProbes int = 9

// applyKeepAlive configures keepalive on a listener's accepted sockets: the
// standard library sets it at Accept, on the connection, from these fields.
// Zero keeps its default, 15 s; negative sets none; a positive value is both the
// idle time and the probe interval, as the loop's engines take it (ADR 0189).
func applyKeepAlive(cfg *stdnet.ListenConfig, d time.Duration) {
	//: the standard library's 15 s.
	if d == 0 {
		return
	}
	//: no keepalive at all.
	if d < 0 {
		cfg.KeepAlive = -1
		return
	}
	cfg.KeepAliveConfig = stdnet.KeepAliveConfig{Enable: true, Idle: d, Interval: d, Count: keepAliveProbes}
}

// applyAcceptedKeepAlive sets keepalive on one accepted socket: the portable
// backend's, as the listener-level path is the goroutine engine's.
func applyAcceptedKeepAlive(tc *stdnet.TCPConn, d time.Duration) {
	//: the standard library's 15 s, set at Accept.
	if d == 0 {
		return
	}
	//: no keepalive.
	if d < 0 {
		swallowErr(tc.SetKeepAlive(false))
		return
	}
	swallowErr(tc.SetKeepAliveConfig(stdnet.KeepAliveConfig{Enable: true, Idle: d, Interval: d, Count: keepAliveProbes}))
}

// portableKeepAlive is the keepalive of a portable group's sockets: the group's,
// negative when the operator switched tcp-keepalive off.
func portableKeepAlive(group *StreamGroup, plan loopPlan) time.Duration {
	//: switched off.
	if slices.Contains(plan.disabled, corenet.CapabilityTCPKeepAlive) {
		//: none.
		return -1
	}
	//: as the group said.
	return group.timeouts.KeepAlive.Duration()
}

// keepAliveOf is the keepalive a stream group's sockets take: the option's,
// or negative when the operator switched tcp-keepalive off.
func (s *Server) keepAliveOf(group *StreamGroup) time.Duration {
	//: switched off by name or by an unknown token.
	if s.operator.kernelDisabled(corenet.CapabilityTCPKeepAlive) {
		//: none.
		return -1
	}
	//: as the group said.
	return group.timeouts.KeepAlive.Duration()
}

// userTimeoutOf is the TCP_USER_TIMEOUT a stream group's listeners take: zero
// when the operator switched tcp-user-timeout off, otherwise resolved as the
// group's option or its write bound.
func (s *Server) userTimeoutOf(group *StreamGroup) time.Duration {
	//: switched off by name or by an unknown token.
	if s.operator.kernelDisabled(corenet.CapabilityTCPUserTimeout) {
		//: none.
		return 0
	}
	//: as resolved.
	return resolveUserTimeout(group)
}

// resolveUserTimeout is what UserTimeout's zero means: the group's write bound.
func resolveUserTimeout(group *StreamGroup) time.Duration {
	//: an explicit value decides.
	if group.timeouts.UserTimeout != 0 {
		//: the option's own.
		return group.timeouts.UserTimeout.Duration()
	}
	//: the group's write bound, zero when it has none.
	return group.timeouts.Write.Duration()
}
