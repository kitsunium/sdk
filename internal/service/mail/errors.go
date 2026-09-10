// Package mail — declares the sentinel *errs.Error outcomes of composition and
// of the SMTP session. Each var's name equals its errs.Define Reason in
// SCREAMING_SNAKE form.
//
// No Public here carries a credential, a server reply string, a host or a
// message. A server's reply text is third-party data of unbounded length that
// this SDK did not write; it travels as a log-only field and never into a
// message a caller might render.
package mail

import "github.com/kitsunium/sdk/internal/kernel/errs"

// exitDataErr matches sysexits EX_DATAERR (65): the message is unusable.
const exitDataErr int = 65

// exitConfig matches sysexits EX_CONFIG (78): a permanent wiring fault. The
// same call will be refused identically forever and the fix is an edit at the
// call site.
const exitConfig int = 78

// exitNoPerm matches sysexits EX_NOPERM (77): a security verdict. A refused
// downgrade and a refused cleartext credential belong here and not among the
// I/O accidents.
const exitNoPerm int = 77

// exitTempFail matches sysexits EX_TEMPFAIL (75): the far side was not
// reachable or not willing right now. Retrying is meaningful.
const exitTempFail int = 75

// exitNoHost matches sysexits EX_NOHOST (68): the named host did not answer.
const exitNoHost int = 68

var (
	// ComposeFailed is returned when the MIME body could not be assembled.
	//
	// It is deliberately rare: everything a caller can get wrong is refused by
	// core/mail's guards with a specific verdict, so reaching this one means an
	// encoder or a media-type parser refused something those guards accepted —
	// which is a defect in this package, not in the caller's message.
	ComposeFailed = errs.Define(CodeComposeFailed, "COMPOSE_FAILED",
		"The message could not be assembled into a MIME body",
		"service/mail: a MIME encoder, a multipart writer or mime.ParseMediaType refused input the core/mail guards accepted — the cause travels as a field",
		errs.WithExitCode(exitDataErr))

	// InvalidConfig is returned by the SMTP constructor for a configuration
	// that cannot be honoured as written.
	//
	// The zero TLSMode is the interesting member. It is REFUSED rather than
	// read as a default, because both candidate defaults are wrong: choosing
	// encryption silently breaks every caller pointing at a plaintext relay on
	// a private network, and choosing none silently ships credentials in the
	// clear for everyone else. ADR 0031's refusing half, applied where the
	// SDK genuinely cannot know.
	InvalidConfig = errs.Define(CodeInvalidConfig, "INVALID_CONFIG",
		"The SMTP transport configuration is not usable as written",
		"service/mail: empty Host, Port outside 1-65535, or TLS left at its zero value — the zero is refused because neither 'encrypt' nor 'do not' is a safe guess (ADR 0031)",
		errs.WithExitCode(exitConfig))

	// DialFailed is returned when the TCP connection was never established, or
	// was severed by the caller's context before the greeting.
	DialFailed = errs.Define(CodeDialFailed, "DIAL_FAILED",
		"The mail server could not be reached",
		"service/mail: TCP dial failed, timed out, or the context was cancelled before the SMTP greeting; the dial error travels as a field",
		errs.WithExitCode(exitNoHost))

	// GreetingFailed is returned when the connection opened and the SMTP
	// conversation did not: a refused greeting, a refused EHLO, a QUIT the
	// server never acknowledged.
	GreetingFailed = errs.Define(CodeGreetingFailed, "GREETING_FAILED",
		"The mail server did not accept the SMTP conversation",
		"service/mail: the 220 greeting, the EHLO/HELO exchange or the closing QUIT was refused or malformed; the server reply travels as a field",
		errs.WithExitCode(exitTempFail))

	// TLSRequired is returned when the transport is configured for STARTTLS
	// and the server never advertised it.
	//
	// This is the defect the domain refuses to have. "Opportunistic STARTTLS"
	// — encrypt if offered, continue in the clear if not — is the shape almost
	// every mail library ships, and it means an active attacker strips the
	// advertisement and reads everything, including the credentials, while the
	// caller's logs show a successful send. There is no configuration here
	// that produces that behaviour, and adding one is not a feature request.
	TLSRequired = errs.Define(CodeTLSRequired, "TLS_REQUIRED",
		"The mail server did not offer the encryption this transport requires",
		"service/mail: TLSStartTLS was configured and the EHLO response advertised no STARTTLS extension; refused, never downgraded to cleartext",
		errs.WithExitCode(exitNoPerm))

	// TLSFailed is returned when STARTTLS or the implicit handshake was
	// attempted and did not complete: a refused command, an untrusted chain, a
	// name that does not match.
	TLSFailed = errs.Define(CodeTLSFailed, "TLS_FAILED",
		"The connection to the mail server could not be encrypted",
		"service/mail: the STARTTLS command was refused, or the TLS handshake failed on trust, name or version; the handshake error travels as a field",
		errs.WithExitCode(exitNoPerm))

	// AuthInsecure is returned when credentials were configured for a session
	// that is not encrypted — at CONSTRUCTION when TLS is disabled outright,
	// and again before the AUTH command when the session turned out not to be
	// encrypted after all.
	//
	// The second check is not redundant, and net/smtp is the reason. Its
	// PlainAuth carves out an exception for a server named localhost —
	// "isLocalhost(server.Name)" — and sends the password in the clear to it.
	// That is defensible for the stdlib's audience and indefensible for a
	// transport in a container, where the relay one hop away IS 127.0.0.1 and
	// the network between them is a bridge somebody else can join.
	AuthInsecure = errs.Define(CodeAuthInsecure, "AUTH_INSECURE",
		"Credentials cannot be sent over an unencrypted mail session",
		"service/mail: Username is set on a session that is not encrypted; refused before the AUTH command is written, including for a localhost relay where net/smtp.PlainAuth would have sent it",
		errs.WithExitCode(exitNoPerm))

	// AuthFailed is returned when the server rejected the credentials, or
	// advertised no AUTH mechanism to offer them through.
	AuthFailed = errs.Define(CodeAuthFailed, "AUTH_FAILED",
		"The mail server rejected the credentials",
		"service/mail: the AUTH exchange was refused, or the EHLO response advertised no AUTH extension; the server reply travels as a field and the credential never leaves this process's memory",
		errs.WithExitCode(exitNoPerm))

	// SendRefused is returned when MAIL, RCPT or DATA was answered with a
	// failure reply.
	//
	// It carries the SMTP reply as a log-only field, never as Public: a reply
	// is third-party text of unbounded length that may name a recipient, and
	// Public is the half that travels to strangers.
	SendRefused = errs.Define(CodeSendRefused, "SEND_REFUSED",
		"The mail server refused the message",
		"service/mail: MAIL FROM, RCPT TO or DATA was answered with a failure reply; the command and the server's reply travel as log-only fields",
		errs.WithExitCode(exitTempFail))
)

// wrapAs returns the given mail sentinel as the error origin — its code,
// reason and public message win — attaching the cause's message and any extra
// fields as structured metadata.
//
// Wrapping the sentinel rather than the cause is what keeps this domain's code
// intact: errs.Wrap is origin-wins, so passing an *errs.Error cause first would
// let it hijack the verdict. A nil cause yields the sentinel with only the
// caller's fields.
func wrapAs(sentinel *errs.Error, cause error, fields ...errs.FieldValue) error {
	//: a nil cause contributes no field — keep only what the caller supplied.
	if cause == nil {
		//: wrap for the fields alone.
		return errs.Wrap(sentinel, errs.WrapParams{}, fields...)
	}
	//: carry the cause message as a log-only field so it stays diagnosable.
	return errs.Wrap(sentinel, errs.WrapParams{}, append(fields, errs.String("cause", cause.Error()))...)
}
