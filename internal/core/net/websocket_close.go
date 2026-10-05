package net

// The close codes RFC 6455 §7.4.1 defines. Three of them — WSCloseNoStatus,
// WSCloseAbnormal and WSCloseTLSHandshake — are values an application may
// OBSERVE but that MUST NOT appear on the wire: they describe the absence of a
// close frame, so sending one would be a statement contradicted by its own
// delivery.
const (
	// WSCloseNormal is a completed purpose, on either side.
	WSCloseNormal WSCloseCode = 1000
	// WSCloseGoingAway is a server shutting down or a browser navigating away.
	// It is what a drain sends.
	WSCloseGoingAway WSCloseCode = 1001
	// WSCloseProtocolError is a frame the RFC forbids.
	WSCloseProtocolError WSCloseCode = 1002
	// WSCloseUnsupportedData is a well-formed frame carrying data this endpoint
	// cannot accept — binary to a text-only peer, for instance.
	WSCloseUnsupportedData WSCloseCode = 1003
	// WSCloseNoStatus means the peer's Close frame carried no code. It is
	// reported to the application and never sent.
	WSCloseNoStatus WSCloseCode = 1005
	// WSCloseAbnormal means the connection died without a Close frame. It is
	// reported to the application and never sent.
	WSCloseAbnormal WSCloseCode = 1006
	// WSCloseInvalidPayload is a text frame that is not valid UTF-8, or a close
	// reason that is not.
	WSCloseInvalidPayload WSCloseCode = 1007
	// WSClosePolicyViolation is the generic refusal when no other code fits.
	WSClosePolicyViolation WSCloseCode = 1008
	// WSCloseTooLarge is a message beyond what this endpoint accepts.
	WSCloseTooLarge WSCloseCode = 1009
	// WSCloseExtensionRequired is a client giving up because the server did not
	// negotiate an extension it needs. A server never sends it.
	WSCloseExtensionRequired WSCloseCode = 1010
	// WSCloseInternalError is an unexpected condition on this side.
	WSCloseInternalError WSCloseCode = 1011
	// WSCloseTLSHandshake means the TLS handshake failed. It is reported to the
	// application and never sent — there is no connection to send it on.
	WSCloseTLSHandshake WSCloseCode = 1015
)

// The boundaries of the close-code ranges RFC 6455 §7.4.2 reserves.
const (
	// wsCloseProtocolLow is the first code the protocol itself may define.
	wsCloseProtocolLow WSCloseCode = 1000
	// wsCloseRegisteredHigh is the last code registered with IANA today
	// (1012 Service Restart, 1013 Try Again Later and 1014 Bad Gateway are
	// later registrations against the same range). Everything between here and
	// wsCloseLibraryLow is reserved-but-unallocated, so it is refused.
	wsCloseRegisteredHigh WSCloseCode = 1014
	// wsCloseUndefined is the one hole inside the assigned block: 1004 was
	// reserved and never given a meaning, so it must not be sent.
	wsCloseUndefined WSCloseCode = 1004
	// wsCloseLibraryLow opens the range libraries and frameworks register
	// first-come-first-served, which runs contiguously into the private range.
	wsCloseLibraryLow WSCloseCode = 3000
	// wsClosePrivateHigh closes the range applications may use without
	// registering anything.
	wsClosePrivateHigh WSCloseCode = 4999
)

// sendable is WSCloseCode.Sendable's body: decl_gen.go writes WSCloseCode.Sendable, from the
// design, as one call of it.
func (c WSCloseCode) sendable() bool {
	//: the assigned block, minus the hole at 1004 and minus the three codes
	//: that describe the absence of a close frame (1005, 1006, 1015).
	if c >= wsCloseProtocolLow && c <= wsCloseRegisteredHigh {
		//: everything assigned is sendable except those three.
		return c != wsCloseUndefined && c != WSCloseNoStatus && c != WSCloseAbnormal
	}
	//: the library range (3000-3999) and the private one (4000-4999) are
	//: contiguous and both open. Everything else — below 1000, the gap above
	//: the registered block that swallows 1015, and past 4999 — is reserved
	//: and unallocated.
	return c >= wsCloseLibraryLow && c <= wsClosePrivateHigh
}

// echoable is WSCloseCode.Echoable's body: decl_gen.go writes WSCloseCode.Echoable, from the
// design, as one call of it.
func (c WSCloseCode) echoable() WSCloseCode {
	//: the only unsendable value that can reach here is "the peer sent none",
	//: and a normal closure is the honest thing to answer: nothing went wrong.
	if !c.Sendable() {
		//: a completed purpose.
		return WSCloseNormal
	}
	//: §5.5.1 — the peer's own code is what it SHOULD receive.
	return c
}
