// Package session declares the server-side session domain: the [Store] port
// that owns a session's lifetime, the opaque [ID] that names it, the immutable
// [SessionValue] a store hands back, and the [Sealer] that renders an [ID] as
// a tamper-evident string. Concrete stores — in memory and on disk — live in
// internal/service/session; this package owns only the contract.
//
// # A session is not a token
//
// The two are routinely confused and the choice has consequences, so this
// domain states the difference rather than leaving it to the reader.
//
// A TOKEN (internal/service/token — JWT, PASETO) is SELF-CONTAINED: the claims
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
package session

import "context"

// Store owns the lifetime of every session it minted. Implementations MUST be
// safe for concurrent use.
//
// The method set is deliberately five operations wide and is FROZEN: pkg/v1
// aliases this interface, so under ADR 0039 a sixth method would break every
// downstream implementer at compile time with no deprecation window. New
// capabilities arrive as SIBLING interfaces discovered by type assertion —
// [Sweeper] is the first, and the model is codec's Appender (ADR 0037).
//
// # Regenerate is the only way to bind a subject
//
// There is no SetSubject, no Store.Elevate, and no [SessionValue] mutator that
// touches the subject. The single call that associates a session with an
// authenticated principal is [Store.Regenerate], and it mints a NEW identifier
// every time. Session fixation — an attacker plants an identifier, the victim
// authenticates, the identifier keeps working — is therefore not a mistake a
// caller can make by forgetting a step: the step IS the login.
//
// IFACE-PLUGIN: the concrete stores stay unexported behind their constructors
// in internal/service/session.
type Store interface {
	// New mints an anonymous session with a fresh identifier and no subject.
	// The identifier is never supplied by the caller: adopting one is the
	// other half of session fixation.
	New(ctx context.Context) (SessionValue, error)
	// Load resolves id to a live session and SLIDES its idle window — an idle
	// timeout that is not refreshed on use is not an idle timeout. It returns
	// [NotFound] for an identifier that names nothing and [Expired] for one
	// whose deadline has passed; an expired record is dropped, so the second
	// call reports [NotFound].
	Load(ctx context.Context, id ID) (SessionValue, error)
	// Save persists the session's DATA. It never writes the subject: a value
	// whose subject differs from the stored record is refused with
	// [FixationRefused] rather than being written or silently ignored.
	Save(ctx context.Context, session SessionValue) error
	// Regenerate mints a new identifier for the session currently named by
	// current, carries its data across, binds subject to it, and destroys the
	// old record. Pass the same subject to rotate without changing principal;
	// pass "" to drop back to anonymous. It refuses with
	// [IdentifierCollision] if the freshly minted identifier already names a
	// live session — a broken entropy source must not silently disable the
	// rotation this call exists to perform.
	Regenerate(ctx context.Context, current ID, subject string) (SessionValue, error)
	// Destroy removes the session named by id. It is idempotent: destroying an
	// identifier that names nothing reports nil, because a caller logging out
	// twice has got what they asked for.
	Destroy(ctx context.Context, id ID) error
}

// Sweeper is the first SIBLING of [Store] (ADR 0039): a store that can drop its
// expired records on demand implements it, and a caller reaches it by type
// assertion rather than by a widened [Store].
//
//	if sweeper, ok := store.(session.Sweeper); ok {
//	    removed, err := sweeper.Sweep(ctx)
//	}
//
// Sweeping is not scheduled here. A store that decided its own cadence would
// own a goroutine the caller never asked for; driving Sweep on a cron or an
// interval is what the scheduler domain (ADR 0041) is for.
type Sweeper interface {
	// Sweep drops every record whose deadline has passed and reports how many
	// it removed. It is safe to call concurrently with any [Store] method.
	Sweep(ctx context.Context) (removed int, err error)
}
