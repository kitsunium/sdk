//go:build windows

// Package server — the two socket families Windows cannot open at all.
package server

// platformLacks reports whether this platform has no socket for a family the
// engine serves everywhere else.
//
// Windows' AF_UNIX (since Windows 10 1803) is STREAM-only. socket() itself
// refuses SOCK_DGRAM and SOCK_SEQPACKET for it — measured on windows-latest,
// a unixgram bind answers `socket: An address incompatible with the requested
// protocol was used` (WSAEAFNOSUPPORT) — so "unixgram" and "unixpacket" name
// sockets that cannot exist here whatever the address, the permissions or the
// moment. Letting the bind fail reported LISTEN_FAILED, which reads as a
// deployment fault an operator could fix; refusing first reports the
// platform's own answer, the same UNSUPPORTED_PLATFORM every missing mechanic
// in the SDK gives (ADR 0018 §(a)). TestWindowsHasNoUnixDatagramOrSeqpacketSocket
// is the measurement this rests on: it fails the day Windows opens either
// family, which is the day this refusal must go.
func platformLacks(network string) bool {
	//: the two AF_UNIX socket types Windows does not implement.
	return network == "unixgram" || network == "unixpacket"
}
