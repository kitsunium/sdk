//go:build !windows

package journald

// unixDatagrams reports whether this platform can open the AF_UNIX datagram
// socket the default dialer connects. Off Windows the kernel answers: a host
// without journald fails the connect with JOURNALD_OPEN_FAILED, as it always
// has.
const unixDatagrams bool = true
