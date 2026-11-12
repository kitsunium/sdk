//go:build openbsd

package loop

// keepAliveParams sets nothing: OpenBSD has no per-socket keepalive time,
// only the system's (net.inet.tcp.keepidle and keepintvl), which the standard
// library leaves as they are there too.
func keepAliveParams(_, _ int) error {
	//: SO_KEEPALIVE alone.
	return nil
}
