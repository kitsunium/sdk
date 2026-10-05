//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/security/session .

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
// A TOKEN (see pkg/v1/security/token — JWT, PASETO) is self-contained: the claims travel
// inside it, signed, and verifying one needs a key and nothing else. That is
// what lets it scale across processes that share no state. It is also why a
// token CANNOT BE REVOKED — nothing stops an issued token but its own expiry,
// so "log out everywhere" means "wait".
//
// A SESSION is an opaque handle. The identifier says nothing; every fact about
// the session lives on the server, in a [Store]. Because the server owns the
// record, [Store].Destroy revokes IMMEDIATELY and the next request fails. The
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
// Here it is not a step. [Store].Regenerate is the ONLY call that binds a
// subject to a session, and it mints a new identifier every time. There is no
// SetSubject, no Session.WithSubject, and no Save that writes a subject:
// [Store].Save compares the subject against the stored record and refuses a
// mismatch with [FixationRefused]. "Keep this identifier and log this user in"
// is a sentence with no method.
//
// After a Regenerate the returned [Session] carries a NEW identifier. Re-issue
// the cookie, or the caller is holding one that no longer resolves.
//
// # Two deadlines, and what happens when they disagree
//
// [Config] takes both an IdleTimeout — how long a session may go untouched,
// refreshed by every [Store].Load — and an AbsoluteTimeout — how long it may
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
// Rotating does not buy time. [Store].Regenerate keeps the original ceiling
// when the subject is unchanged, so rotating on a timer cannot produce an
// immortal session. A rotation that CHANGES the subject restarts both clocks,
// because a privilege boundary produces a genuinely new session and handing it
// the remaining seconds of the anonymous browsing before it would log a user
// out moments after they signed in.
//
// # The identifier is a secret
//
// [ID] is 256 bits from crypto/rand. It redacts itself in %v, %s and %#v;
// [ID].Reveal is the only way to its canonical string and is spelled to read
// like a mistake anywhere that is not building a cookie; [ID].Equal compares in
// constant time; and [ID].Digest is what a store indexes and names files by, so
// the secret is never a map key, never a filename and never at rest.
//
// # Where the SDK stops
//
// This package ships the STORE and the SEALING. It does not ship a firewall,
// access voters, a login convention, or anything that writes an HTTP header.
// [Sealer].Seal produces the cookie's VALUE; choosing the cookie's name, Path,
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
// files by [ID].Digest so no identifier is ever on disk, requires an owner-only
// directory and ASSERTS the mode it gets rather than assuming it, publishes
// every write through a temporary file and rename(2), and serialises every
// read-modify-write under one exclusive lock.
//
// The directory is a path, and other accounts may write parts of it, so the
// store refuses to be led anywhere through it. Before creating anything it
// walks every component of FileConfig.Dir and refuses, with [PathRedirected],
// a symbolic link planted in a directory any account can write — the shape
// /tmp has — while honouring one only a trusted account could have planted,
// as /tmp is on macOS. It then holds the directory open for its lifetime and
// resolves every name against that handle, so a parent renamed or replaced
// afterwards moves nothing. And it never reads a record, nor takes its lock,
// through a link planted at a file's own name: a link at the lock file is
// [PathRedirected], a link at a record is [RecordCorrupt] like any other
// tampering, and nothing is created through either.
//
// Waiting for that lock is ABANDONABLE, in both halves: the flock is taken
// without blocking and retried every FileConfig.Poll on the injected clock,
// and the in-process gate is a channel rather than a mutex. A blocking flock(2)
// parks the thread inside a syscall no cancellation can reach, so a request
// whose client had hung up kept waiting for a lock nobody would read the
// result of. A caller whose context ends gets StoreUnavailable, carrying its
// own context error.
//
// Those guarantees rest on flock(2), on a filesystem that enforces Unix
// permissions, and on a directory that can be flushed so a rename or an unlink
// survives a power cut. Where they do not exist — Windows, which has no
// directory flush and on which the SDK builds no owner-only access control
// list, wasip1, and any other GOOS without flock(2) — [NewFileStore] returns
// the SDK's [UnsupportedPlatform] sentinel AT CONSTRUCTION rather than
// building a store that would report success while providing none of it (ADR
// 0018). Use [NewMemoryStore], an external store, or another host.
//
// # Sweeping is yours to schedule
//
// Both stores implement [Sweeper], reached by type assertion rather than
// through a wider [Store] (ADR 0039). Neither runs a background goroutine the
// caller never asked for; driving Sweep on a cadence is what pkg/v1/app/scheduler
// is for. The file store additionally implements io.Closer, which releases its
// lock descriptor and the directory it holds.
package session
