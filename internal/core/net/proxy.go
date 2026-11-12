package net

// ProxyProtocolValue configures the PROXY protocol on a TCP listener group,
// behind an L4 balancer that announces the real client (HAProxy's protocol,
// v1 and v2 both, chosen by the header's first byte).
//
// The header is believed from a peer whose real address falls in one of Trusted's
// ranges and from nobody else: any other peer is closed before a byte of it is
// read. A trusted peer must send a header, within Timeout: a connection without
// one, or with a malformed one, is refused. There is no "accept both", so a
// listener cannot be half-fooled by a peer that sends a header it was not told
// to. The address announced is what RemoteAddr reports, Admission's key
// included; a LOCAL command or an UNKNOWN family keeps the socket's own.
type ProxyProtocolValue struct {
	// Trusted lists the CIDRs of the balancers. Empty, or a range that does not
	// parse, fails Start with OptionValueInvalid.
	Trusted []string `json:"trusted"`
	// Timeout bounds the header's arrival, on the server's clock. Zero selects
	// 3 s; a negative value fails Start with OptionValueInvalid.
	Timeout DurationValue `json:"timeout"`
}
