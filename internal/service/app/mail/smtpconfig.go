// Package mail — the SMTP transport's configuration, and its zero value.
package mail

import (
	"cmp"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// defaultDialTimeout bounds the TCP connection when the caller sets none and
// the context carries no deadline. It CLAMPS rather than refuses: unlike a TLS
// mode, "wait forever" is not a policy anybody chooses on purpose, and thirty
// seconds needs no explanation (ADR 0031's clamping half).
const defaultDialTimeout time.Duration = 30 * time.Second

// maxPort is the highest TCP port number.
const maxPort int = 65535

// defaultLocalName is the EHLO name net/smtp itself uses when given none. A
// submission server ignores which one it receives.
const defaultLocalName string = "localhost"

// TLSMode selects how the session is encrypted. Its ZERO VALUE IS REFUSED.
//
// That refusal is the security decision of this package. Every other choice
// would be wrong for somebody: defaulting to encryption breaks the caller
// pointing at a plaintext relay inside a private network, and defaulting to
// none ships every other caller's credentials in the clear. ADR 0031 admits a
// clamp where one value needs no explanation and demands a refusal where any
// SDK-chosen value would be arbitrary; this is unambiguously the second.
//
// There is deliberately NO opportunistic mode — "encrypt if the server offers
// it, continue in the clear if it does not". It is the shape most mail
// libraries ship and it means an active attacker who strips one line from the
// EHLO response reads the whole session, credentials included, while the
// sender's logs show a successful delivery. It cannot be configured here
// because it is not implemented, not because it is off by default.
type TLSMode uint8

const (
	// TLSUnset is the zero value and is refused at construction.
	TLSUnset TLSMode = iota
	// TLSStartTLS connects in the clear, sends EHLO, and REQUIRES the server
	// to advertise STARTTLS (RFC 3207). A server that does not is refused with
	// [TLSRequired] — the session is never continued unencrypted. This is the
	// submission port 587 shape.
	TLSStartTLS
	// TLSImplicit negotiates TLS before the SMTP greeting, which RFC 8314 §3.3
	// recommends over STARTTLS because there is no plaintext phase for an
	// attacker to interfere with. This is the submissions port 465 shape.
	TLSImplicit
	// TLSDisabled sends everything in the clear, and must be spelled out loud.
	// It exists for a relay on a loopback or a private segment where TLS is
	// somebody else's layer. Credentials are REFUSED with it at construction:
	// there is no combination of settings in this package that sends a
	// password over an unencrypted socket.
	TLSDisabled
)

// String renders the mode for a log or an error field.
func (m TLSMode) String() string {
	//: a closed enum, so a switch is total and needs no default value invented.
	switch m {
	//: the submission port 587 shape.
	case TLSStartTLS:
		//: RFC 3207.
		return "starttls"
	//: the submissions port 465 shape.
	case TLSImplicit:
		//: RFC 8314 §3.3.
		return "implicit"
	//: the opt-out, which has to be spelled out loud.
	case TLSDisabled:
		//: named, because that is the point of it.
		return "disabled"
	//: the zero value, which every constructor refuses.
	case TLSUnset:
		//: the refused zero.
		return "unset"
	//: anything else is an integer nobody in this package minted.
	default:
		//: not a mode.
		return "invalid"
	}
}

// SMTPConfig configures the SMTP transport.
//
// Everything refusable about it is refused by [NewSMTP] at CONSTRUCTION rather
// than at send time: an unset TLS mode, a port outside the protocol's range, or
// credentials the transport would have to send in the clear. The fix for each
// is one line in the wiring, which is where the error is raised.
type SMTPConfig struct {
	// Host is the server's name. It is also the name TLS verifies the
	// certificate against, and the name EHLO is answered by — so it is not
	// interchangeable with an IP address.
	Host string
	// Port is the TCP port: 587 for submission with STARTTLS, 465 for
	// submissions with implicit TLS (RFC 8314 §3.1), 25 for a relay.
	Port int
	// TLS selects the encryption mode. The zero value is refused.
	TLS TLSMode
	// Identity carries the trust anchors, the client certificate for mutual
	// TLS, and the minimum protocol version. It is core/net's opaque identity,
	// reused rather than twinned: a mail server's TLS is the same TLS every
	// other outbound connection in this SDK uses, and a second configuration
	// type would be a second place for a minimum version to be wrong.
	//
	// Its zero value is usable and means "the platform trust store, TLS 1.3
	// minimum". [SMTPConfig.Host] is used as the verified name when the
	// identity names none.
	Identity corenet.IdentityValue
	// Username is the SASL authentication identity. Empty means no AUTH
	// command is issued at all.
	Username string
	// Password is the secret for Username. It never appears in an error, a
	// field or a log line emitted by this package.
	Password string
	// LocalName is the domain given in EHLO. Empty selects "localhost", which
	// is what net/smtp uses and what a submission server ignores.
	LocalName string
	// DialTimeout bounds the TCP connection. Non-positive clamps to
	// [defaultDialTimeout]; a shorter context deadline still wins.
	DialTimeout time.Duration
}

// validate refuses a configuration that cannot be honoured as written.
func (c SMTPConfig) validate() error {
	//: the endpoint first: a transport with no reachable server is one that
	//: cannot be wired later either.
	if endpointErr := c.validateEndpoint(); endpointErr != nil {
		//: InvalidConfig.
		return endpointErr
	}
	//: then the encryption policy and the credentials it does or does not allow.
	return c.validateSecurity()
}

// validateEndpoint refuses a host or a port the transport could never dial.
func (c SMTPConfig) validateEndpoint() error {
	//: a transport with no server is a transport that cannot be wired later.
	if c.Host == "" {
		//: InvalidConfig, at construction.
		return errs.Wrap(InvalidConfig, errs.WrapParams{}, errs.String("problem", "empty host"))
	}
	//: a port outside the protocol's range is a typo, not a policy.
	if c.Port <= 0 || c.Port > maxPort {
		//: the port is the caller's own literal and is safe to report.
		return errs.Wrap(InvalidConfig, errs.WrapParams{},
			errs.String("problem", "port out of range"), errs.Int("port", c.Port))
	}
	//: dialable.
	return nil
}

// validateSecurity refuses an unset or invented TLS mode, and any credential
// the configured mode would have to send in the clear.
func (c SMTPConfig) validateSecurity() error {
	//: the zero TLS mode — the refusal this type exists for.
	if c.TLS == TLSUnset {
		//: refused rather than guessed (ADR 0031).
		return errs.Wrap(InvalidConfig, errs.WrapParams{},
			errs.String("problem", "TLS mode is unset; choose TLSStartTLS, TLSImplicit or TLSDisabled explicitly"))
	}
	//: an invented enum value is not a mode this package implements.
	if c.TLS > TLSDisabled {
		//: refused; the numeric value is diagnostic and carries no secret.
		return errs.Wrap(InvalidConfig, errs.WrapParams{},
			errs.String("problem", "unknown TLS mode"), errs.Int("tls_mode", int(c.TLS)))
	}
	//: credentials over a session that is unencrypted BY CONFIGURATION are
	//: refused here, where the fix is one line, rather than at send time.
	if c.Username != "" && c.TLS == TLSDisabled {
		//: AuthInsecure, and the password is not in the error.
		return errs.Wrap(AuthInsecure, errs.WrapParams{},
			errs.String("problem", "Username is set and TLS is disabled"))
	}
	//: a password with no identity to present it as cannot be sent.
	if c.Username == "" && c.Password != "" {
		//: InvalidConfig; the password itself never appears.
		return errs.Wrap(InvalidConfig, errs.WrapParams{},
			errs.String("problem", "Password is set without a Username"))
	}
	//: honourable as written.
	return nil
}

// resolvedDialTimeout applies the clamp.
func (c SMTPConfig) resolvedDialTimeout() time.Duration {
	//: a non-positive timeout is an unfilled field, and "forever" is not what
	//: anybody meant by leaving it blank.
	if c.DialTimeout <= 0 {
		//: clamped.
		return defaultDialTimeout
	}
	//: the caller's own bound.
	return c.DialTimeout
}

// resolvedLocalName applies net/smtp's own default.
func (c SMTPConfig) resolvedLocalName() string {
	//: EHLO needs a name and a submission server ignores which one, so the
	//: empty case takes what net/smtp would have used.
	return cmp.Or(c.LocalName, defaultLocalName)
}
