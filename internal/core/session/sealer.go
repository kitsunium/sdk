// Package session — the port that renders an identifier as a cookie value.
package session

// Sealer turns an [ID] into a tamper-evident, confidential string and back.
// Implementations MUST be safe for concurrent use.
//
// # What it is for, and where it stops
//
// A session identifier can be handed to a client as-is: it is already 256 bits
// of opaque randomness. Sealing adds two things a raw identifier cannot have.
// It BINDS the value to a key and a purpose, so a value minted for one
// application — or for one purpose within it — is refused by another rather
// than being looked up and missed. And it makes the value UNREADABLE in
// transit and at rest on the client, so an identifier does not sit in plain
// text in a browser's cookie jar, a proxy log, or a crash report.
//
// It produces the cookie's VALUE. It does not write a cookie. Nothing in this
// domain knows what an HTTP response is, and that boundary is deliberate: the
// name, Path, Domain, Max-Age, Secure, HttpOnly and SameSite attributes are
// decisions about a request, and the layer that understands requests is the
// framework.
//
//	sealed, err := sealer.Seal(session.ID())
//	if err != nil {
//	    return err
//	}
//	http.SetCookie(w, &http.Cookie{ // the framework's line, not the SDK's
//	    Name: "sid", Value: sealed,
//	    HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
//	})
//
// # Open is not an oracle
//
// Every failure — a truncated value, a flipped bit, the right ciphertext under
// the wrong key, the right key with the wrong purpose — returns the SAME
// [SealInvalid] verdict with no detail. Distinguishing them would tell an
// attacker which half of a forgery attempt was already correct.
//
// # The seal is not a token
//
// Nothing but the identifier goes inside. No subject, no expiry, no claims: a
// sealed identifier that carried its own expiry would be a token, and would
// have a token's revocation problem. Every fact about the session is read from
// the [Store], which is what makes [Store.Destroy] effective immediately.
//
// IFACE-PLUGIN: the concrete sealer stays unexported behind its constructor in
// internal/service/session.
type Sealer interface {
	// Seal renders id as an opaque string safe to use verbatim as a cookie
	// value. A zero id is refused with [InvalidID].
	Seal(id ID) (sealed string, err error)
	// Open reverses Seal. Every failure returns [SealInvalid] and a zero ID.
	Open(sealed string) (id ID, err error)
}
