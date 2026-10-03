// Package mail — one open SMTP conversation.
package mail

import (
	"net/smtp"
)

// session is one open SMTP conversation: the client, and the configuration that
// decides what is allowed to happen on it.
//
// It exists so that the policy steps take no *smtp.Client parameter. That is
// not only a matter of shape: keeping the client and the configuration together
// is what makes "check the ACTUAL TLS state before writing AUTH" a property of
// the session rather than an argument a later refactor can drop.
type session struct {
	transport *smtpTransport
	cfg       SMTPConfig
	client    *smtp.Client
}
