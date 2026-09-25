// Package mail — reading an SMTP URL into an SMTPConfig.
package mail

import (
	"net/url"
	"strconv"
	"strings"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// The two schemes ParseURL reads, and the one parameter.
const (
	// schemeSMTP is submission: STARTTLS unless the tls parameter says
	// otherwise.
	schemeSMTP string = "smtp"
	// schemeSMTPS is submissions: implicit TLS, which the tls parameter may
	// only repeat.
	schemeSMTPS string = "smtps"
	// tlsParameter is the only query parameter understood.
	tlsParameter string = "tls"
)

// The default port of each mode: RFC 8314 §3.3 submission with STARTTLS, §3.1
// submissions with implicit TLS, and the relay port for a plaintext relay.
const (
	// portStartTLS is submission (RFC 6409).
	portStartTLS int = 587
	// portImplicit is submissions (RFC 8314 §3.1).
	portImplicit int = 465
	// portRelay is SMTP relay (RFC 5321).
	portRelay int = 25
)

var (
	// tlsModesByName is the tls parameter's closed vocabulary.
	tlsModesByName = map[string]TLSMode{
		"starttls": TLSStartTLS,
		"implicit": TLSImplicit,
		"none":     TLSDisabled,
	}
	// defaultPorts is the port each mode listens on when the URL names none.
	defaultPorts = map[TLSMode]int{
		TLSStartTLS: portStartTLS,
		TLSImplicit: portImplicit,
		TLSDisabled: portRelay,
	}
)

// ParseURL reads an SMTP URL into an SMTPConfig that [NewSMTP] accepts:
//
//	smtp://user:password@host:587?tls=starttls|implicit|none
//	smtps://user:password@host:465
//
// The scheme sets the default mode — smtp:// is STARTTLS, the direction that
// fails loudly (TLS_REQUIRED) against a server that cannot encrypt rather than
// sending in the clear; smtps:// is implicit TLS — and the tls parameter
// overrides it, except that smtps:// may only repeat "implicit": a URL that
// says both is a contradiction, refused rather than resolved. A plaintext
// session must be spelled tls=none. The port defaults by mode (587, 465, 25)
// and must be 1–65535. The userinfo is optional and percent-decoded, so a
// password with reserved characters is written %40 for "@".
//
// The result is checked exactly as NewSMTP checks a configuration, so a URL
// with credentials and tls=none fails here with [AuthInsecure] rather than at
// construction. On any failure it returns the zero SMTPConfig.
//
// A refusal is [InvalidURL] with a clause, and it NEVER quotes the URL or any
// part of it — the userinfo is the password, and a URL that failed to parse is
// the one whose parts are not where they should be. url.Parse's own message
// quotes its input and is therefore dropped.
func ParseURL(raw string) (cfg SMTPConfig, err error) {
	parsed, parseErr := url.Parse(raw)
	//: not a URL, or an opaque one (smtp:host) with no authority to read.
	if parseErr != nil || parsed.Opaque != "" {
		//: the parser's message quotes the input; only the clause travels.
		return SMTPConfig{}, invalidURL("not a URL of the form smtp://user:password@host:port")
	}
	mode, modeErr := modeOf(parsed)
	//: an unknown scheme, a bad or contradictory tls parameter.
	if modeErr != nil {
		//: InvalidURL.
		return SMTPConfig{}, modeErr
	}
	//: an SMTP URL names a server and nothing below it.
	if (parsed.Path != "" && parsed.Path != "/") || parsed.Fragment != "" || parsed.RawFragment != "" {
		//: InvalidURL.
		return SMTPConfig{}, invalidURL("has a path or a fragment")
	}
	port, portErr := portOf(parsed.Hostname(), parsed.Port(), mode)
	//: no host, or a port out of range.
	if portErr != nil {
		//: InvalidURL.
		return SMTPConfig{}, portErr
	}
	cfg = SMTPConfig{Host: parsed.Hostname(), Port: port, TLS: mode}
	//: the userinfo is optional; the password is percent-decoded by url.Parse.
	if parsed.User != nil {
		cfg.Username = parsed.User.Username()
		cfg.Password, _ = parsed.User.Password()
	}
	//: what NewSMTP would refuse is refused here, with NewSMTP's verdicts.
	if validateErr := cfg.validate(); validateErr != nil {
		//: InvalidConfig or AuthInsecure; the config is not handed back.
		return SMTPConfig{}, validateErr
	}
	//: a configuration NewSMTP accepts.
	return cfg, nil
}

// modeOf reads the TLS mode from the scheme and the tls parameter, refusing an
// unknown scheme, any parameter but tls, a repeated or unknown tls value, and
// an smtps:// URL asking for anything but implicit TLS.
func modeOf(parsed *url.URL) (mode TLSMode, err error) {
	query, queryErr := url.ParseQuery(parsed.RawQuery)
	//: a malformed escape in the query.
	if queryErr != nil {
		//: InvalidURL.
		return TLSUnset, invalidURL("has a malformed query")
	}
	//: tls is the whole vocabulary; a stranger is refused, not ignored.
	for name := range query {
		//: the name is not repeated: it may be anything a caller typed.
		if name != tlsParameter {
			//: InvalidURL.
			return TLSUnset, invalidURL("has a query parameter other than tls")
		}
	}
	//: the scheme's own default.
	switch strings.ToLower(parsed.Scheme) {
	//: submission.
	case schemeSMTP:
		mode = TLSStartTLS
	//: submissions.
	case schemeSMTPS:
		mode = TLSImplicit
	//: anything else is not an SMTP URL.
	default:
		//: InvalidURL.
		return TLSUnset, invalidURL("scheme is not smtp or smtps")
	}
	values := query[tlsParameter]
	//: no parameter: the scheme decides.
	if len(values) == 0 {
		//: the default.
		return mode, nil
	}
	//: said twice is a contradiction, even when both say the same thing.
	if len(values) > 1 {
		//: InvalidURL.
		return TLSUnset, invalidURL("names the tls parameter more than once")
	}
	named, known := tlsModesByName[strings.ToLower(values[0])]
	//: a mode this package does not implement.
	if !known {
		//: InvalidURL.
		return TLSUnset, invalidURL("has a tls parameter that is not starttls, implicit or none")
	}
	//: smtps:// already says implicit; saying otherwise is a contradiction.
	if mode == TLSImplicit && named != TLSImplicit {
		//: InvalidURL.
		return TLSUnset, invalidURL("uses smtps with a tls parameter other than implicit")
	}
	//: the parameter's mode.
	return named, nil
}

// portOf reads the URL's port, defaulting by mode, and refuses a URL with no
// host or a port outside 1–65535.
func portOf(host, written string, mode TLSMode) (port int, err error) {
	//: a server with no name cannot be dialled or verified.
	if host == "" {
		//: InvalidURL.
		return 0, invalidURL("names no host")
	}
	//: no port: the mode's own.
	if written == "" {
		//: the default by mode.
		return defaultPorts[mode], nil
	}
	port, convErr := strconv.Atoi(written)
	//: url.Parse has checked the digits; the range is this package's.
	if convErr != nil || port < 1 || port > maxPort {
		//: InvalidURL, without the number: it is part of the URL.
		return 0, invalidURL("has a port that is not a number from 1 to 65535")
	}
	//: the URL's port.
	return port, nil
}

// invalidURL is the InvalidURL verdict with its clause.
func invalidURL(problem string) error {
	//: the clause, and nothing from the URL.
	return errs.Wrap(InvalidURL, errs.WrapParams{}, errs.String("problem", problem))
}
