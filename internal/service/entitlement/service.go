// Package entitlement - orchestration: turn local key material plus a signed
// roster into a grant, or refuse. The fetch is attempted on every cold
// verification and the only thing on disk allowed to stand in for it is a
// bundle this machine already authenticated, read back through the same
// signature and freshness checks — never a decision, always the same document.
package entitlement

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	coreent "github.com/kitsunium/sdk/internal/core/entitlement"
)

// maxArtefactBytes caps each roster artefact. The endpoint is untrusted by
// construction, so an unbounded read hands it a memory-exhaustion lever; a
// roster of thousands of subjects still fits far inside this.
const maxArtefactBytes int64 = 4 << 20

// fetchTimeout bounds ONE attempt at ONE origin. It is short on purpose:
// with several origins tried in turn, a generous per-origin timeout stacks
// into a wait no user would sit through, and a black-holed endpoint is far
// better abandoned than waited on. The overall bound is this times the
// number of origins, which is what makes adding an origin cheap.
const fetchTimeout time.Duration = 3 * time.Second

// BearerFetch performs the one request in this package that carries a
// credential.
//
// A function rather than an interface: only this request needs an
// Authorization header, and widening Getter would put a bearer parameter on
// the roster fetch, which must never send one. Naming it as a single-method
// interface would also force the -er form of GetWithBearer, which is not a
// word.
// Getter performs the roster HTTP GETs. Injected so tests exercise the
// admission logic without a network.
type Getter interface {
	// Get retrieves a URL.
	Get(url string) (*http.Response, error)
}

type BearerFetch func(url, bearer string) (resp *http.Response, err error)

// Service verifies entitlement.
//
// It DOES keep the last bundle it authenticated, which reverses half of an
// earlier decision — "it holds no cached roster on purpose: a disk cache able
// to authorize would let a frozen file (or a frozen clock) keep a revoked
// subject running forever". The frozen file is answered: the cached bytes go
// back through ParseBundle on every read, so a frozen copy stops authorising
// at its own ExpiresAt, at most coreent.RosterLifetime after it was signed. The frozen
// clock is not answered and cannot be, here or anywhere else in an offline
// binary — it was not answered before the cache existed either. cache.go
// carries the full argument, next to the code it justifies.
type Service struct {
	// client performs the roster fetches.
	client Getter
	// identity is how this machine proves who it is. A port, because the one
	// implementation that reads ssh key material needs a vendor dependency
	// this module bans — see core/entitlement's package comment.
	identity coreent.Identity
	// vendor is the only trust anchor, linked into the binary.
	vendor []byte
	// origins is where the roster is looked for, in order. Held on the
	// Service rather than read from the package global so a test can point
	// it at a stub server without touching process-wide state.
	origins []coreent.OriginValue
	// product names the vendor whose roster this Service trusts, and scopes
	// the cache, the CI audience and the enrolment URL. It carries what the
	// source implementation kept as package constants.
	product *ProductValue
	// version is this binary's own version, compared against the roster's
	// mandatory-update floor. Empty disables the check, which is what a
	// caller that has no version to declare gets.
	version string
	// bearerFetch performs the one request that carries a credential: the
	// Actions token mint. Held on the Service so a test can exercise the CI
	// path without a network, and so the redirect-refusing default is what
	// production gets without every caller having to remember it.
	bearerFetch BearerFetch
	// cacheDir is where the last authenticated bundle is kept, or "" to
	// disable the offline fallback entirely. Only NewService fills it: a
	// Service built around an injected getter must not reach a real
	// filesystem unless a test says so.
	cacheDir string
	// timeServers are the Roughtime servers consulted to corroborate the
	// local clock. Empty disables the check, which is what committed source
	// ships and what every test constructor gets: a Service must not reach
	// the network for time unless somebody asked it to.
	timeServers []RoughtimeServerValue
}

// WithTimeServers points the clock corroboration at a set of Roughtime servers,
// or disables it with an empty list.
//
// Separate from the constructor because it is off by default and because the
// list is a trust decision — each entry pins a public key — that belongs to
// whoever assembled the binary rather than to this package.
func (s *Service) WithTimeServers(servers []RoughtimeServerValue) *Service {
	s.timeServers = servers
	//: Return the receiver so construction reads as one expression.
	return s
}

// WithBearerFetch replaces the transport used for the Actions token mint.
//
// Only a test should need this. The default refuses redirects, which is what
// keeps the runner's credential from being forwarded to a destination
// checkTokenURL never vouched for.
func (s *Service) WithBearerFetch(fetch BearerFetch) *Service {
	s.bearerFetch = fetch
	//: Return the receiver so construction reads as one expression.
	return s
}

// WithOrigins replaces the publication points this verifier will try.
//
// It changes WHERE the roster is looked for and nothing about whether the
// answer is believed: authority comes from the vendor signature, never from the
// origin that served the bytes, so pointing this at a hostile endpoint gains
// that endpoint nothing — it can serve whatever it likes and still cannot forge
// the anchor. That is what makes the setter safe to expose at all.
//
// An empty list is a construction error rather than a way to disable the fetch,
// and currentRoster says so rather than reporting an unreachable roster nobody
// asked for.
func (s *Service) WithOrigins(origins []coreent.OriginValue) *Service {
	s.origins = origins
	//: Return the receiver so construction reads as one expression.
	return s
}

// WithVersion records the binary's own version so Verify can apply the
// roster's mandatory-update floor.
//
// It is a separate call rather than a constructor parameter because
// pkg/license must not import the command package that owns the version
// string, and because a caller with nothing to declare — a test, a tool
// embedding the check — should not have to invent one.
func (s *Service) WithVersion(version string) *Service {
	s.version = version
	//: Return the receiver so construction reads as one expression.
	return s
}

// NewService builds a verifier over the caller's ssh directory.
func NewService(identity coreent.Identity, vendor []byte, product *ProductValue) *Service {
	//: Callers get a ready verifier with a bounded HTTP client and the
	//: offline fallback armed. Defaulting it HERE rather than at each call
	//: site is what keeps the gate, the daemon watchdog and `license status`
	//: from disagreeing about whether this machine has a grace window.
	return &Service{
		client:      &http.Client{Timeout: fetchTimeout},
		identity:    identity,
		vendor:      vendor,
		product:     product,
		origins:     product.Origins,
		bearerFetch: DefaultBearerFetch,
		cacheDir:    product.DefaultCacheDir(),
		timeServers: RoughtimeServers,
	}
}

// NewServiceWithGetter builds a verifier over an injected getter.
func NewServiceWithGetter(client Getter, identity coreent.Identity, vendor []byte, product *ProductValue) *Service {
	//: Callers get a verifier whose network layer they control.
	return &Service{client: client, identity: identity, vendor: vendor, product: product, origins: product.Origins, bearerFetch: DefaultBearerFetch}
}

// NewServiceWithOrigins builds a verifier over an injected getter and an
// explicit origin list, so a test can exercise the fallback order — which
// origin wins, and what happens when the first few are unusable — without
// naming production endpoints.
func NewServiceWithOrigins(client Getter, identity coreent.Identity, vendor []byte, origins []coreent.OriginValue) *Service {
	//: Callers get a verifier whose network layer AND publication points
	//: they control.
	return &Service{client: client, identity: identity, vendor: vendor, origins: origins, bearerFetch: DefaultBearerFetch}
}

// Verify performs a full cold verification: fetch, authenticate, match, and
// prove possession. Every call reaches for the network first — this is the
// path a fresh process takes — and when no origin answers it falls back to the
// last bundle this machine authenticated, which is refused by the same
// signature and window rules that would refuse it over HTTPS. A machine that
// has never fetched one successfully still cannot start.
//
// The ORDER below is load-bearing, and neither the offline fallback nor the
// clock ratchet disturbs it. The ratchet runs FIRST, before anything reads a
// date: a function that is about to compare four deadlines against the local
// clock should establish that the clock has not been moved before it starts,
// not after. The
// mandatory-update floor runs BEFORE the subject is looked up on purpose: an
// out-of-date binary must be told to upgrade whether or not its licence is
// also in order, and the upgrade path is the same either way. Because the
// cached roster is substituted inside currentRoster rather than around it,
// that floor applies to a cached document exactly as it does to a fetched one
// — which is what stops an out-of-date binary from skipping it by going
// offline, including by going offline with a copy in hand.
func (s *Service) Verify(now time.Time) (grant coreent.GrantValue, err error) {
	//: Outside CI an absent key is both the likeliest answer and the most
	//: useful one, and saying it costs no network round trip. Inside CI it is
	//: not an answer at all yet: a runner is not expected to hold a key, so
	//: the seat is what decides, and the discovery failure is only reported
	//: if that path does not work out either.
	subject, discoverErr := s.identity.Discover()
	//: Refuse early only when there is no CI path to try.
	if discoverErr != nil && !InCI() {
		//: Propagate the absent-licence case.
		return coreent.GrantValue{}, discoverErr
	}

	//: Before any timestamp is trusted, doubt the thing that produces them.
	//: Every deadline below is compared against a clock the holder owns, so a
	//: clock that has moved backwards past the newest instant the vendor has
	//: ever signed to this machine is refused here rather than believed for
	//: the rest of the function.
	if clockErr := s.checkClockAndTime(now); clockErr != nil {
		//: Propagate the regressed clock; setting it is what resolves this.
		return coreent.GrantValue{}, clockErr
	}

	roster, offline, rosterErr := s.currentRoster(now)
	//: A roster we cannot authenticate leaves us unable to decide.
	if rosterErr != nil {
		//: Propagate the unreachable, forged or stale roster.
		return coreent.GrantValue{}, rosterErr
	}

	//: The floor is carried by the same signed document, so an out-of-date
	//: binary cannot skip it by going offline nor forge its way past it.
	if RequiresUpdate(s.version, roster.RequiredVersion) {
		//: Name both versions; "from what, to what" is the only question.
		return coreent.GrantValue{}, UpdateRefusal(s.version, roster.RequiredVersion)
	}

	//: Which of the two entitlements answers is a decision of its own, and
	//: keeping it out of here is what leaves this function as the ORDER —
	//: which is the part that is load-bearing.
	authorised, authErr := s.authorise(roster, subject, discoverErr, now)
	//: Propagate whatever refused.
	if authErr != nil {
		//: Refused.
		return coreent.GrantValue{}, authErr
	}

	//: Recorded in ONE place, on whichever grant came back, so a CI seat and
	//: a device grant can never disagree about where the roster came from.
	authorised.Offline = offline
	//: Entitled.
	return authorised, nil
}

// authorise resolves the two ways a roster can entitle this process: a proven
// CI run, which costs no device seat, or the enrolled identity.
//
// discoverErr is carried in rather than re-derived because Verify deliberately
// DEFERS it: outside CI an absent key refuses immediately, but inside Actions a
// runner is not expected to hold one, so the seat is allowed to answer first
// and the discovery failure is only reported if it does not.
func (s *Service) authorise(roster *coreent.RosterValue, subject string, discoverErr error, now time.Time) (grant coreent.GrantValue, err error) {
	//: A proven CI run costs no device seat, so it is tried first.
	ciGrant, ciErr := s.ciSeat(roster, now)
	//: Authorised without spending a seat.
	if ciErr == nil {
		//: Entitled as CI.
		return ciGrant, nil
	}
	//: A failure here is normally NOT a refusal — a runner that also holds a
	//: device key must still be able to use it, so it falls through. The one
	//: exception is a roster that says otherwise, which is the only place
	//: "this run must be authorised as CI" can be stated without letting the
	//: party being checked state it.
	if ciRefusalIsFinal(roster) {
		//: The vendor required a seat and there is none.
		return coreent.GrantValue{}, ciErr
	}

	//: No CI seat, so an identity is required after all.
	if discoverErr != nil {
		//: Propagate the absent-licence case, naming the CI half too when
		//: there was one: on a runner, "no licence key found" on its own
		//: sends an operator to `license create` on a machine that will never
		//: hold a key.
		return coreent.GrantValue{}, ciContext(discoverErr, ciErr)
	}

	//: Matching then proving is what turns a published key into an identity.
	matched, matchErr := s.matchSubject(roster, subject, now)
	//: Propagate the revocation, mismatch or possession failure.
	if matchErr != nil {
		//: Refused; same reasoning as above for the CI annotation.
		return coreent.GrantValue{}, ciContext(matchErr, ciErr)
	}

	//: Carry the deadline, not just the instant: a daemon aging against
	//: VerifiedAt alone kept serving past the roster window that authorised
	//: it, and past the subject's own term. Both bounds are known right
	//: here and nowhere else, so this is where they have to be recorded.
	return coreent.GrantValue{
		Subject:    subject,
		VerifiedAt: now,
		NotAfter:   coreent.GrantDeadline(now, roster.ExpiresAt, matched.ExpiresAt),
	}, nil
}

// currentRoster fetches and authenticates the published roster, trying each
// origin until one yields a roster that both verifies and is still inside
// its window, and falling back to the last one this machine authenticated
// when no origin can be reached.
//
// "First that answers" would be wrong: an origin whose publisher has stalled
// still serves a perfectly well-signed roster whose window has closed, and
// taking it would refuse a licence the next origin would have approved. So a
// stale or forged answer is treated exactly like an unreachable one — move
// on — and only the last failure is reported if every origin fails.
//
// The second result says which of the two answered. It is not a permission
// level: an offline roster passed the identical checks, and everything
// downstream — the update floor, the CI seat, the subject match, the grant
// deadline — is applied to it unchanged. It exists so the refusal, the grant
// and the operator staring at either can say where the answer came from
// instead of a cached roster being indistinguishable from a fresh one.
func (s *Service) currentRoster(now time.Time) (roster *coreent.RosterValue, offline bool, err error) {
	//: An empty origin list is a construction error, not a network one; say
	//: so rather than reporting an unreachable roster nobody ever asked for.
	if len(s.origins) == 0 {
		//: Refuse with a cause an operator can act on.
		return nil, false, fmt.Errorf("%w: no origin configured", coreent.ErrRosterUnreachable)
	}

	var lastErr error
	//: Try each publication point in turn; the first usable roster wins.
	for _, origin := range s.origins {
		candidate, originErr := s.rosterFrom(origin, now)
		//: A roster that verified and is in-window ends the search.
		if originErr == nil {
			//: Authorised by this origin.
			return candidate, false, nil
		}
		//: Name the origin so a total failure says which endpoints were
		//: tried and how each one failed.
		lastErr = fmt.Errorf("%s: %w", origin.Name, originErr)
	}

	//: No origin answered. A bundle this machine already authenticated is
	//: not a lower bar — it is the same signed document, re-read through the
	//: same ParseBundle, and refused by the same window rules.
	if cached, cacheErr := s.cachedRoster(now); cacheErr == nil {
		//: Authorised by the cache, and said so.
		return cached, true, nil
	}

	//: Report the NETWORK failure, not the cache one. The cache is the
	//: fallback: an operator told "no cached roster at ~/.cache/..." would be
	//: sent to fix the wrong thing, when what actually happened is that two
	//: publication points were unreachable AND no usable copy was on hand.
	return nil, false, lastErr
}

// rosterFrom fetches and authenticates the roster from one origin.
//
// One GET, not two. The roster and its signature travel in a single bundle
// precisely so they cannot arrive out of step: served as separate objects
// they were cached independently for five minutes each, and a new signature
// over an old roster is indistinguishable from a forgery — a correct client
// refusing a correct roster, with the scheme's most alarming error, for
// roughly 8% of all wall-clock time.
func (s *Service) rosterFrom(origin coreent.OriginValue, now time.Time) (roster *coreent.RosterValue, err error) {
	raw, fetchErr := s.fetch(origin.BundleURL)
	//: A bundle we cannot read leaves us unable to decide; refuse.
	if fetchErr != nil {
		//: Propagate the unreachable case.
		return nil, fetchErr
	}
	//: Decoding, authentication and freshness all happen before any field is
	//: trusted.
	parsed, parseErr := ParseBundle(raw, s.vendor, now)
	//: Nothing worth keeping: forged, stale, or not a bundle at all.
	if parseErr != nil {
		//: Propagate the refusal.
		return nil, parseErr
	}

	//: Keep the bytes HERE, at the one moment they are known to be the
	//: vendor's and known to be in-window — and keep them whether or not the
	//: subject match that follows succeeds, because a roster that revoked
	//: this machine is exactly the roster the next offline start should read.
	s.rememberRoster(raw, parsed)
	//: Authenticated, fresh, and remembered.
	return parsed, nil
}

// matchSubject checks the local key against what the roster publishes for
// this subject, rejects a subject whose own term has closed, then requires a
// fresh possession proof. Order matters: the cheap comparisons run before the
// signing round trip.
func (s *Service) matchSubject(roster *coreent.RosterValue, subject string, now time.Time) (matched coreent.SubjectValue, err error) {
	want, lookupErr := roster.SubjectFor(subject)
	//: Absence from a verified roster reports as revoked; SubjectFor's own
	//: doc explains why that sentinel also covers "never approved".
	if lookupErr != nil {
		//: Propagate the refusal.
		return coreent.SubjectValue{}, lookupErr
	}
	//: A subject can outlive its own term even while the roster itself is
	//: fresh — the two dates bound different things. A zero ExpiresAt means
	//: no term was recorded, not an instantly-closed one.
	if !want.ExpiresAt.IsZero() && now.After(want.ExpiresAt) {
		//: Refuse a subject whose individual term has closed.
		return coreent.SubjectValue{}, fmt.Errorf("%w: %s", coreent.ErrLicenseExpired, subject)
	}

	got, printErr := s.identity.Fingerprint(subject)
	//: A subject listed upstream but with no local material cannot be matched.
	if printErr != nil {
		//: Propagate the absent-licence case.
		return coreent.SubjectValue{}, printErr
	}
	//: The published fingerprint is the identity; a local key that does not
	//: match it belongs to someone else. Byte equality, never a parse: the
	//: roster's spelling is the contract.
	if got != want.Fingerprint {
		//: Refuse an identity the roster does not recognise.
		return coreent.SubjectValue{}, fmt.Errorf("%w: %s", coreent.ErrKeyMismatch, subject)
	}

	//: Possession is the last gate and the one that makes publication safe:
	//: without it a caller holds only material the roster hands to everyone.
	if proveErr := s.identity.ProvePossession(subject); proveErr != nil {
		//: Holding the published half proves nothing; refuse.
		return coreent.SubjectValue{}, proveErr
	}
	//: Hand back the roster's record so the caller can bound the grant by
	//: this subject's own term as well as by the roster's window.
	return want, nil
}

// regularFile reports whether path is a regular file, following symlinks.
//
// Following them is deliberate and applies to BOTH halves of a pair: a key
// symlinked in from a password manager or a mounted volume is an ordinary
// setup, and refusing it would break installs that work today. What must be
// refused is a path that could never have held a key — a directory, a socket,
// a FIFO — because those stat without error and would otherwise count as an
// identity.
func regularFile(path string) bool {
	info, statErr := os.Stat(path)
	//: An unreadable entry cannot be a usable half either way.
	return statErr == nil && info.Mode().IsRegular()
}

// closeBestEffort releases a reader without masking the caller's own error.
func closeBestEffort(c io.Closer, what string) {
	//: Best-effort Close: log and continue on failure.
	if cerr := c.Close(); cerr != nil {
		log.Printf("close %s: %v", what, cerr)
	}
}

// fetch retrieves one roster artefact, mapping every failure to
// coreent.ErrRosterUnreachable so callers can distinguish "cannot decide" from
// "decided no".
func (s *Service) fetch(url string) (body []byte, err error) {
	resp, getErr := s.client.Get(url)
	//: A transport failure means no decision is possible.
	if getErr != nil {
		//: Report the unreachable roster.
		return nil, fmt.Errorf("%w: %s: %w", coreent.ErrRosterUnreachable, url, getErr)
	}
	//: Prevent a leaked connection on every path.
	defer closeBestEffort(resp.Body, url)

	//: Anything but 200 is an unusable answer, including a 404 from a
	//: repository that no longer publishes the roster.
	if resp.StatusCode != http.StatusOK {
		//: Report the unreachable roster.
		return nil, fmt.Errorf("%w: %s: status %d", coreent.ErrRosterUnreachable, url, resp.StatusCode)
	}

	//: The same bounded read the cache path uses, so "a bundle off the disk
	//: goes through the same door as a bundle off the wire" is true at the
	//: byte level and not merely at the signature check.
	return readBounded(resp.Body, url)
}

// readBounded reads at most maxArtefactBytes from r, refusing anything larger
// rather than buffering it.
//
// Shared by the network fetch and the cache read because BOTH sources are
// untrusted: an endpoint by construction, and a cache file because it sits on a
// disk its holder controls. An unbounded read hands either of them a
// memory-exhaustion lever, and having the rule in one place is what stops the
// two paths from drifting apart — which they already had, the cache reading
// with a plain os.ReadFile while the fetch was capped.
//
// what names the source in the diagnostics: a URL, or a path.
func readBounded(r io.Reader, what string) (body []byte, err error) {
	//: Read one byte past the cap so "at the cap" and "over the cap" are
	//: distinguishable without a second syscall.
	data, readErr := io.ReadAll(io.LimitReader(r, maxArtefactBytes+1))
	//: A truncated read cannot be authenticated.
	if readErr != nil {
		//: Report the unusable artefact.
		return nil, fmt.Errorf("%w: %s: %w", coreent.ErrRosterUnreachable, what, readErr)
	}
	//: An artefact past the cap is either corrupt or hostile; either way it
	//: is not a roster we should try to authenticate.
	if int64(len(data)) > maxArtefactBytes {
		//: Report the unusable artefact.
		return nil, fmt.Errorf("%w: %s: larger than %d bytes", coreent.ErrRosterUnreachable, what, maxArtefactBytes)
	}
	//: Return the raw artefact for signature verification.
	return data, nil
}
