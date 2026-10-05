package mail

import (
	"cmp"
	"time"

	coremail "github.com/kitsunium/sdk/internal/core/app/mail"
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

const (
	// TLSUnset is the zero value and is refused at construction.
	TLSUnset TLSMode = iota
	// TLSStartTLS connects in the clear, sends EHLO, and REQUIRES the server
	// to advertise STARTTLS (RFC 3207). A server that does not is refused with
	// [coremail.TLSRequired] — the session is never continued unencrypted. This is the
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
		return errs.Wrap(coremail.InvalidConfig, errs.WrapParams{}, errs.String("problem", "empty host"))
	}
	//: a port outside the protocol's range is a typo, not a policy.
	if c.Port <= 0 || c.Port > maxPort {
		//: the port is the caller's own literal and is safe to report.
		return errs.Wrap(coremail.InvalidConfig, errs.WrapParams{},
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
		return errs.Wrap(coremail.InvalidConfig, errs.WrapParams{},
			errs.String("problem", "TLS mode is unset; choose TLSStartTLS, TLSImplicit or TLSDisabled explicitly"))
	}
	//: an invented enum value is not a mode this package implements.
	if c.TLS > TLSDisabled {
		//: refused; the numeric value is diagnostic and carries no secret.
		return errs.Wrap(coremail.InvalidConfig, errs.WrapParams{},
			errs.String("problem", "unknown TLS mode"), errs.Int("tls_mode", int(c.TLS)))
	}
	//: credentials over a session that is unencrypted BY CONFIGURATION are
	//: refused here, where the fix is one line, rather than at send time.
	if c.Username != "" && c.TLS == TLSDisabled {
		//: AuthInsecure, and the password is not in the error.
		return errs.Wrap(coremail.AuthInsecure, errs.WrapParams{},
			errs.String("problem", "Username is set and TLS is disabled"))
	}
	//: a password with no identity to present it as cannot be sent.
	if c.Username == "" && c.Password != "" {
		//: InvalidConfig; the password itself never appears.
		return errs.Wrap(coremail.InvalidConfig, errs.WrapParams{},
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
