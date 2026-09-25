// Package mail — how an SMTPConfig renders: every rendering the password could
// leak through writes the placeholder instead.
package mail

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	coresecret "github.com/kitsunium/sdk/internal/core/secret"
)

// viewTypeName and configTypeName are the two spellings of the struct's name
// %#v produces: the view's, and the one a reader of the rendering expects.
const (
	// viewTypeName is how fmt names smtpConfigView in Go syntax.
	viewTypeName string = "mail.smtpConfigView"
	// configTypeName is how the rendering names the type it describes.
	configTypeName string = "mail.SMTPConfig"
)

// smtpConfigView is SMTPConfig's field set with no methods. Formatting a value
// of it reaches fmt's default struct rendering instead of recursing into
// SMTPConfig's own Format, and the conversion between the two only compiles
// while their fields are identical — so a field added to SMTPConfig and not
// here breaks the build rather than escaping the redaction.
type smtpConfigView struct {
	// Host mirrors SMTPConfig.Host.
	Host string
	// Port mirrors SMTPConfig.Port.
	Port int
	// TLS mirrors SMTPConfig.TLS.
	TLS TLSMode
	// Identity mirrors SMTPConfig.Identity, which redacts itself.
	Identity corenet.IdentityValue
	// Username mirrors SMTPConfig.Username.
	Username string
	// Password holds the placeholder when a password is set.
	Password string
	// LocalName mirrors SMTPConfig.LocalName.
	LocalName string
	// DialTimeout mirrors SMTPConfig.DialTimeout.
	DialTimeout time.Duration
}

// redacted is the configuration with its password replaced by the placeholder
// every secret in the SDK renders as. An EMPTY password stays empty: that no
// password is set is not a secret, and a rendering that showed a placeholder
// for nothing would mislead whoever debugs a failed AUTH.
func (c SMTPConfig) redacted() smtpConfigView {
	view := smtpConfigView(c)
	//: a set password is never rendered, whatever its length.
	if view.Password != "" {
		view.Password = coresecret.Redacted
	}
	//: every other field as it is.
	return view
}

// String renders the configuration with field names and the password
// redacted — the %+v form.
func (c SMTPConfig) String() string {
	//: fmt's own struct rendering of the redacted view.
	return fmt.Sprintf("%+v", c.redacted())
}

// GoString renders the configuration in Go syntax with the password redacted,
// named as the type it is.
func (c SMTPConfig) GoString() string {
	rendered := fmt.Sprintf("%#v", c.redacted())
	//: the view's name is an implementation detail; the reader asked about an
	//: SMTPConfig.
	return configTypeName + strings.TrimPrefix(rendered, viewTypeName)
}

// Format implements fmt.Formatter, so EVERY verb — %v, %+v, %#v, %s, %q, %x —
// formats the redacted view with the flags and width it was given. Without it,
// %+v of an SMTPConfig printed the password: String and GoString are consulted
// by only some verbs, and a struct with neither prints every field.
func (c SMTPConfig) Format(state fmt.State, verb rune) {
	//: Go syntax names the type as SMTPConfig, not as the view.
	if verb == 'v' && state.Flag('#') {
		writeRendering(state, c.GoString())
		//: nothing else to write.
		return
	}
	//: every other directive, reconstructed and applied to the view.
	writeRendering(state, fmt.Sprintf(fmt.FormatString(state, verb), c.redacted()))
}

// MarshalJSON implements json.Marshaler with the password redacted, so a
// configuration dumped as JSON — a --show-config flag, a diagnostics page —
// does not carry it. The shape is otherwise the one encoding/json produced
// before this method existed: the same field names, the same types.
func (c SMTPConfig) MarshalJSON() (encoded []byte, err error) {
	//: the default encoding of the redacted view.
	return json.Marshal(c.redacted())
}

// writeRendering writes a rendering into the state fmt handed to Format.
// fmt.Formatter has no error return and fmt drops what a write into its own
// buffer reports, so the error is read and dropped here, visibly.
func writeRendering(state fmt.State, text string) {
	//: fmt's buffer is the only destination, and it does not fail.
	if _, err := io.WriteString(state, text); err != nil {
		//: nothing to report to — fmt drops the same error.
		return
	}
}
