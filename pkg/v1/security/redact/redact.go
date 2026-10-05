package redact

import (
	svcredact "github.com/kitsunium/sdk/internal/service/security/redact"
)

// New returns a Redactor applying cfg.
func New(cfg Config) Redactor {
	//: delegate verbatim to the service constructor.
	return svcredact.NewRedactor(cfg)
}
