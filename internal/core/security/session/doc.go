// Package session — ranges 0.2.14.* (the port's verdicts) and 0.3.46.* (the
// engines' own refusals) — ADR 0045, declared here since ADR 0160.
//
// Package session — declares the sentinel *errs.Error outcomes: the verdicts
// of the port, and the refusals specific to the engines in
// internal/service/security/session (ADR 0160). Each var's name equals its
// errs.Define Reason in SCREAMING_SNAKE form.
//
// Every Public string here is written on the assumption that a third party
// reads it: none of them contains an identifier, a subject, a data key, a
// directory path, or a count. The identifier in particular is a bearer secret,
// so it never appears in a Public, in a Private, or in a Field.
//
// Package session — the opaque, redacting session identifier.
//
// Package session — the port that renders an identifier as a cookie value.
//
// Package session declares the server-side session domain: the [Store] port
// that owns a session's lifetime, the opaque [ID] that names it, the immutable
// [SessionValue] a store hands back, and the [Sealer] that renders an [ID] as
// a tamper-evident string. Concrete stores — in memory and on disk — live in
// internal/service/security/session; this package owns only the contract.
//
// # A session is not a token
//
// The two are routinely confused and the choice has consequences, so this
// domain states the difference rather than leaving it to the reader.
//
// A TOKEN (internal/service/security/token — JWT, PASETO) is SELF-CONTAINED: the claims
// travel inside it, signed. Verification needs a key and nothing else — no
// lookup, no shared state, no round trip. That is why a token scales across
// processes that share nothing. It is also why a token CANNOT BE REVOKED: the
// only thing that stops an issued token is its own expiry, so "log out
// everywhere" means "wait".
//
// A SESSION is an OPAQUE HANDLE: the identifier carries no information at all,
// and every fact about the session — who it belongs to, when it was created,
// what the application stored on it — lives on the server, in a [Store].
// Because the server owns the record, [Store.Destroy] revokes IMMEDIATELY and
// the next request fails. The price is state: a store, its availability, and a
// lookup on the request path.
//
// Pick a token when verification must be stateless and a few minutes of
// residual validity are acceptable. Pick a session when "log out now" has to
// mean now, when the data is too large or too private to hand to the client,
// or when the identifier must reveal nothing.
//
// # What this domain does NOT do
//
// The SDK ships the STORE and the SEALING. It does not ship a firewall, access
// voters, a login convention, or anything that writes an HTTP header. [Sealer]
// produces the cookie's VALUE; deciding the cookie's name, Path, SameSite,
// Secure and HttpOnly attributes — and calling http.SetCookie — belongs to the
// framework, which is the layer that knows what a request is. See the package
// CLAUDE.md, §The frontier.
//
// # There is no registry
//
// Like proc (ADR 0016), resilience (ADR 0026), net (ADR 0029), scheduler
// (ADR 0041) and token (ADR 0042), this domain has no name->implementation
// registry. A registry earns its place when an open set of interchangeable
// schemes is selected by data. Here the set is closed and the choice is a
// deployment decision made in code: a memory store is per-process and dies with
// it, a file store survives a restart and is confined to one host. Resolving
// that from a configuration string would let a typo silently downgrade a
// persistent store to an ephemeral one.
//
// Package session — the immutable session a Store hands back.
//
// Package session — the description a Store fills in to build a SessionValue.
package session
