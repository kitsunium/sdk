//go:build windows

// Package journald — the journal socket's family, which Windows does not have.
package journald

// unixDatagrams reports whether this platform can open the AF_UNIX datagram
// socket the default dialer connects. Windows' AF_UNIX is stream-only: a
// unixgram socket is refused by socket() itself, whatever the path — measured
// on windows-latest as WSAEAFNOSUPPORT ("an address incompatible with the
// requested protocol was used"). So the default dialer is refused before it is
// asked, with the SDK's UNSUPPORTED_PLATFORM rather than a connect failure
// that reads like a journal that is down.
const unixDatagrams bool = false
