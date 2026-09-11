//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/session .

// Package session is the public facade for the SDK's server-side session
// domain: an opaque identifier, a store that owns its lifetime, and a sealer
// that renders it as a cookie value.
//
//	store, err := session.NewMemoryStore(session.Config{
//	    IdleTimeout:     30 * time.Minute,
//	    AbsoluteTimeout: 12 * time.Hour,
//	})
//	if err != nil {
//	    return err // a zero timeout is refused HERE, not at the first login
//	}
//
//	anon, err := store.New(ctx)                       // a visitor arrives
//	live, err := store.Load(ctx, id)                  // …and comes back
//	live, err = store.Regenerate(ctx, id, "user-42")  // …and logs in
//	err = store.Destroy(ctx, live.ID())               // …and logs out, now
//
// # A session is not a token
//
// The two are constantly confused and the choice has consequences.
//
// A TOKEN (see pkg/v1/token — JWT, PASETO) is self-contained: the claims travel
// inside it, signed, and verifying one needs a key and nothing else. That is
// what lets it scale across processes that share no state. It is also why a
// token CANNOT BE REVOKED — nothing stops an issued token but its own expiry,
// so "log out everywhere" means "wait".
//
// A SESSION is an opaque handle. The identifier says nothing; every fact about
// the session lives on the server, in a [Store]. Because the server owns the
// record, [Store.Destroy] revokes IMMEDIATELY and the next request fails. The
// price is state: a store, its availability, and a lookup on the request path.
//
// Reach for a token when verification must be stateless and a few minutes of
// residual validity are acceptable. Reach for a session when "log out now" has
// to mean now, when the data is too large or too private to hand to the client,
// or when the identifier must reveal nothing.
//
// # Logging in is Regenerate, and there is no other way to spell it
//
// Session fixation is the attack where somebody plants an identifier, waits for
// the victim to authenticate, and finds that the identifier they planted now
// belongs to an authenticated user. The defence is to rotate the identifier at
// the privilege boundary — and the reason the bug is common is that rotating is
// usually a separate step somebody has to remember.
//
// Here it is not a step. [Store.Regenerate] is the ONLY call that binds a
// subject to a session, and it mints a new identifier every time. There is no
// SetSubject, no Session.WithSubject, and no Save that writes a subject:
// [Store.Save] compares the subject against the stored record and refuses a
// mismatch with [FixationRefused]. "Keep this identifier and log this user in"
// is a sentence with no method.
//
// After a Regenerate the returned [Session] carries a NEW identifier. Re-issue
// the cookie, or the caller is holding one that no longer resolves.
//
// # Two deadlines, and what happens when they disagree
//
// [Config] takes both an IdleTimeout — how long a session may go untouched,
// refreshed by every [Store.Load] — and an AbsoluteTimeout — how long it may
// live at all, measured from the instant its identifier was minted.
//
// The effective deadline is always the EARLIER of the two. The absolute
// timeout is therefore a ceiling: sliding can move the deadline earlier, never
// past it, so a session that is used continuously still dies. Both are
// required, both are refused when non-positive, and IdleTimeout must be
// strictly shorter than AbsoluteTimeout — an idle window the ceiling makes
// unreachable is a sliding expiry the caller believes they configured and does
// not have (ADR 0031).
//
// Rotating does not buy time. [Store.Regenerate] keeps the original ceiling
// when the subject is unchanged, so rotating on a timer cannot produce an
// immortal session. A rotation that CHANGES the subject restarts both clocks,
// because a privilege boundary produces a genuinely new session and handing it
// the remaining seconds of the anonymous browsing before it would log a user
// out moments after they signed in.
//
// # The identifier is a secret
//
// [ID] is 256 bits from crypto/rand. It redacts itself in %v, %s and %#v;
// [ID.Reveal] is the only way to its canonical string and is spelled to read
// like a mistake anywhere that is not building a cookie; [ID.Equal] compares in
// constant time; and [ID.Digest] is what a store indexes and names files by, so
// the secret is never a map key, never a filename and never at rest.
//
// # Where the SDK stops
//
// This package ships the STORE and the SEALING. It does not ship a firewall,
// access voters, a login convention, or anything that writes an HTTP header.
// [Sealer.Seal] produces the cookie's VALUE; choosing the cookie's name, Path,
// Domain, Max-Age, Secure, HttpOnly and SameSite — and calling http.SetCookie —
// belongs to the framework, which is the layer that knows what a request is.
//
//	sealed, err := sealer.Seal(live.ID())
//	if err != nil {
//	    return err
//	}
//	http.SetCookie(w, &http.Cookie{ // your line, not the SDK's
//	    Name: "sid", Value: sealed,
//	    HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
//	})
//
// # Two stores
//
// [NewMemoryStore] keeps everything in this process and dies with it: the right
// store for a single-process service, a test, or a development run.
//
// [NewFileStore] keeps one sealed file per session in one directory. It seals
// every record under an AEAD key, binds each record to its own filename, names
// files by [ID.Digest] so no identifier is ever on disk, requires an owner-only
// directory and ASSERTS the mode it gets rather than assuming it, publishes
// every write through a temporary file and rename(2), and serialises every
// read-modify-write under one exclusive lock.
//
// Waiting for that lock is ABANDONABLE, in both halves: the flock is taken
// without blocking and retried every FileConfig.Poll on the injected clock,
// and the in-process gate is a channel rather than a mutex. A blocking flock(2)
// parks the thread inside a syscall no cancellation can reach, so a request
// whose client had hung up kept waiting for a lock nobody would read the
// result of. A caller whose context ends gets StoreUnavailable, carrying its
// own context error.
//
// Those guarantees rest on flock(2) and on a filesystem that enforces Unix
// permissions. Where they do not exist — Windows, wasip1, and any other GOOS
// without flock(2) — [NewFileStore] returns the SDK's [UnsupportedPlatform]
// sentinel AT CONSTRUCTION rather than building a store that would report
// success while providing neither (ADR 0018). Use [NewMemoryStore], an external
// store, or another host.
//
// # Sweeping is yours to schedule
//
// Both stores implement [Sweeper], reached by type assertion rather than
// through a wider [Store] (ADR 0039). Neither runs a background goroutine the
// caller never asked for; driving Sweep on a cadence is what pkg/v1/scheduler
// is for. The file store additionally implements io.Closer, which releases its
// lock descriptor.
package session

import (
	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	coresession "github.com/kitsunium/sdk/internal/core/session"
	svcsession "github.com/kitsunium/sdk/internal/service/session"
)

// IDLen is the identifier length in bytes — 256 bits from crypto/rand.
const IDLen int = coresession.IDLen

// ID is the public alias for the opaque, redacting session identifier.
type ID = coresession.ID

// Session is the public alias for the immutable session a Store hands back.
type Session = coresession.SessionValue

// State is the public alias for the exported description [NewSession] builds a
// [Session] from. It exists so a framework can implement [Store] over its own
// backend; it is not a way around the fixation rule, since [Store.Save] refuses
// a subject the stored record does not have.
type State = coresession.StateValue

// Store is the public alias for the session-lifetime port. Its method set is
// frozen: new capabilities arrive as sibling interfaces (ADR 0039).
type Store = coresession.Store

// Sweeper is the public alias for the first such sibling — a store that can
// drop its expired records on demand.
type Sweeper = coresession.Sweeper

// Sealer is the public alias for the port that renders an [ID] as an opaque,
// tamper-evident cookie VALUE. It does not write cookies.
type Sealer = coresession.Sealer

// Config is the public alias for the memory store's construction parameters.
type Config = svcsession.Config

// FileConfig is the public alias for the file store's construction parameters.
// Its Key field takes a pkg/v1/crypto.Key, and its Clock is a clock.Timed
// because the store both stamps time and waits on it — the poll between lock
// attempts is armed on it, so a test drives contention without sleeping.
type FileConfig = svcsession.FileConfig

var (
	// NotFound is returned for an identifier that names no live session.
	NotFound = coresession.NotFound
	// Expired is returned when the record existed and its deadline had passed.
	// The record is dropped as this is reported, so the next call says NotFound.
	Expired = coresession.Expired
	// InvalidID is returned for a malformed or zero identifier.
	InvalidID = coresession.InvalidID
	// InvalidConfig is returned by every constructor for a configuration it
	// cannot honour — a non-positive timeout, an idle window the ceiling makes
	// unreachable, a missing directory or key.
	InvalidConfig = coresession.InvalidConfig
	// IdentifierCollision is returned when a freshly minted identifier already
	// names a live session, which at 256 bits means the random source is broken.
	IdentifierCollision = coresession.IdentifierCollision
	// EntropyFailed is returned when the random source could not be read.
	EntropyFailed = coresession.EntropyFailed
	// StoreUnavailable is returned when the backend itself failed. Retryable.
	StoreUnavailable = coresession.StoreUnavailable
	// SealInvalid is returned by Sealer.Open for every failure, without
	// distinguishing them.
	SealInvalid = coresession.SealInvalid
	// FixationRefused is returned by Save when the value's subject differs from
	// the stored record's. Regenerate is the operation that was wanted.
	FixationRefused = coresession.FixationRefused
	// RecordCorrupt is returned by the file store when a record could not be
	// read back — tampered, truncated, wrong key, or filed under another digest.
	RecordCorrupt = svcsession.RecordCorrupt
	// DirectoryUnsafe is returned when the store location is readable beyond
	// its owner, or when the filesystem accepted a 0600 request without
	// enforcing it.
	DirectoryUnsafe = svcsession.DirectoryUnsafe
	// LockFailed is returned when the store-wide exclusive lock could not be
	// taken; the operation is refused rather than run unserialised.
	LockFailed = svcsession.LockFailed
	// PayloadTooLarge is returned by Save for a payload above the store's caps,
	// and by Regenerate for a subject longer than 4096 bytes — refused before
	// anything is minted, so the old session is untouched.
	PayloadTooLarge = svcsession.PayloadTooLarge
	// InvalidPurpose is returned by NewSealer for an empty purpose.
	InvalidPurpose = svcsession.InvalidPurpose
	// UnsupportedPlatform is returned by NewFileStore on a platform without the
	// mechanics its guarantees rest on. It is the SDK-wide sentinel, shared
	// with pkg/v1/proc (ADR 0018).
	UnsupportedPlatform = coreproc.UnsupportedPlatform
)

// NewMemoryStore returns a Store keeping every session in this process's
// memory. It refuses a Config it cannot honour and never returns an inert
// store.
func NewMemoryStore(cfg Config) (store Store, err error) {
	//: delegate to the service constructor.
	return svcsession.NewMemoryStore(cfg)
}

// NewFileStore returns a Store keeping one sealed file per session in cfg.Dir.
// It refuses — at construction — an unusable configuration, an unsafe
// directory, and a platform without flock(2) and enforced Unix permissions.
func NewFileStore(cfg FileConfig) (store Store, err error) {
	//: delegate to the service constructor.
	return svcsession.NewFileStore(cfg)
}

// NewSealer returns a Sealer binding key and purpose. purpose is required: it
// is the domain separator between two things sealed under one key, and an empty
// one is refused rather than read as "no separation needed".
func NewSealer(key corecrypto.Key, purpose string) (sealer Sealer, err error) {
	//: delegate to the service constructor.
	return svcsession.NewSealer(key, purpose)
}

// ParseID decodes the canonical form produced by [ID.Reveal] — unpadded
// base64url over exactly [IDLen] bytes. It is the inbound path: whatever
// arrives from a cookie reaches the domain through here, and anything that is
// not exactly that shape is refused before it can be used as a lookup key.
func ParseID(encoded string) (id ID, err error) {
	//: delegate to the core constructor.
	return coresession.ParseID(encoded)
}

// NewID builds an ID from exactly [IDLen] raw bytes. Most callers want
// [ParseID]; this is for a Store implementation that holds raw identifier
// bytes.
func NewID(raw []byte) (id ID, err error) {
	//: delegate to the core constructor.
	return coresession.NewID(raw)
}

// NewSession builds an immutable [Session] from a [State]. It is what a
// framework implementing its own [Store] returns; ordinary callers get sessions
// from a store and never call this.
func NewSession(state State) (session Session, err error) {
	//: delegate to the core constructor.
	return coresession.NewSessionValue(state)
}
