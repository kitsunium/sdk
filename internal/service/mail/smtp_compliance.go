// Package mail — compile-time port conformance for the two transports.
//
// These assertions live apart from the implementations because a failure here
// is a statement about the PORT, not about a function.
package mail

import coremail "github.com/kitsunium/sdk/internal/core/mail"

var (
	// The SMTP transport implements the frozen port.
	_ coremail.Transport = (*smtpTransport)(nil)
	// It also implements the batching sibling, which is its reason for
	// existing: one session for N messages amortises the handshake.
	_ coremail.BatchSender = (*smtpTransport)(nil)
	// The in-memory transport implements all three, which is what lets a
	// consumer wire it wherever a batching transport is expected.
	_ coremail.FullTransport = (*memoryTransport)(nil)
)

// The SMTP transport deliberately does NOT implement coremail.Outbox: a
// production transport that kept every message it ever sent would be a memory
// leak with an interface on it. TestCapabilitiesAreSiblingsAndNotMembers in
// internal/core/mail is the guard for the port half of that claim.
