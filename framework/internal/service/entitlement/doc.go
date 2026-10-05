// Package entitlement — the trust anchors this verifier accepts, and why there
// is more than one of them.
//
// # The rotation that had no in-band path
//
// One anchor is one key with no way out. An installation whose anchor must
// change — because the vendor's signing key is being retired, or because it was
// compromised — had no path in band: rosters signed by a new key B are refused
// against the anchor A the binary links in, BEFORE anything reads the version
// floor that would have told it to upgrade; and a release signed by B is refused
// by the very installation that needs it. Blocked from both sides, with the only
// remaining route out of band — an operator fetching a binary by hand.
//
// Accepting an ORDERED LIST of anchors is what opens that path: publish B, and
// the installations carrying {A, B} take it while those carrying only {A} keep
// reading A's rosters until they are updated. Neither side has to be updated
// first, which is the whole of what rotation needs.
//
// # The tension, stated rather than hidden
//
// Every anchor on the list is a key whose compromise is ACCEPTED for as long as
// it stays there. A list is therefore strictly more surface than a single key,
// and the mitigation is not a mechanism — it is that the list is ORDERED, BOUNDED
// at maxAnchors, and decided at BUILD time. Nothing at runtime can add to it: no
// roster field, no environment variable, no cache file. A compromised key leaves
// the list the way it entered — by shipping a build without it — and the bound is
// what keeps that a decision somebody has to make rather than a slot that is
// always free.
//
// Package entitlement - the published bundle: roster and signature in ONE
// document, because two documents cannot be fetched atomically.
//
// Package entitlement - the offline fallback: re-presenting a roster this machine
// already authenticated, under exactly the freshness rules that would apply to
// the same bytes coming off the wire.
//
// This reverses half of a documented decision, so it states which half.
// Service used to hold no cache at all, on the grounds that "a disk cache able
// to authorize would let a frozen file (or a frozen clock) keep a revoked
// subject running forever". The frozen FILE is answered here and was never the
// risk it looked like: the cached bytes are the vendor's own signed bundle,
// they go back through ParseBundle on every read, and coreent.ParseRoster refuses a
// window wider than coreent.RosterLifetime, one that has not opened, and one that has
// closed. A frozen file therefore stops authorising at its own ExpiresAt —
// which is the bound errors.go already advertises for coreent.ErrRosterStale ("it
// bounds how long a revoked client keeps working offline"), a sentence that
// described nothing until this file existed.
//
// The frozen CLOCK is NOT answered, and no local mechanism can answer it. Every
// source of time an offline process can read — the system clock, file mtimes,
// a monotonic counter that dies at reboot — belongs to the party being checked.
// It was not answered before this file either: a captured bundle served from a
// local origin, behind a trust store the holder controls, with the clock parked
// inside its window, ran the binary indefinitely without any cache. What this
// file changes for such a holder is the effort, not the outcome; what it
// changes for an honest one is that a laptop off the network keeps working
// until the last roster it saw expires.
//
// Package entitlement - exclusion over the cache directory, which is the one
// thing write-then-rename does not provide.
//
// Renaming a staged file over an installed one is atomic for a READER: nobody
// ever observes half a bundle at the name they look for. It says nothing about
// two WRITERS, and the ratchet is not a write — it is a read, a comparison and
// a write, and the comparison is only worth anything if nothing moves between
// it and the write it guards.
//
// The exclusion is also what lets a refresh land on Windows. There, replacing
// a file another handle holds open is refused, and every file Go opens is held
// that way: syscall.Open asks for FILE_SHARE_READ|FILE_SHARE_WRITE and never
// FILE_SHARE_DELETE. Keeping readers and the writer off the bundle at the same
// time removes the collision rather than retrying past it. Sharing DELETE from
// the read side is NOT an alternative: measured on windows-latest, MoveFileEx
// still refuses a destination held with FILE_SHARE_DELETE — see
// Test_windowsRenameOverAnOpenDestination, which asserts all three share modes
// on a real kernel, and ADR 0079's Deferred section for what is still open.
//
// Package entitlement - the read side of the cache guard where the kernel
// already provides what the guard would buy.
//
// Package entitlement - the read side of the cache guard where the kernel
// does not provide it, and a reader is what breaks a writer.
//
// Package entitlement - obtaining the proof that a run is CI.
//
// The token itself is fetched from the runner, which means the URL and the
// bearer credential both arrive as environment variables — attacker-controlled
// input by the same standard as everything else this package reads. They are
// validated before use rather than trusted because of where they came from.
//
// Package entitlement - authorising a CI run instead of a device.
//
// A proven CI run costs no device seat, so it is tried before the device path.
// Every failure here is silent and falls through: not being in CI, having no
// token, not being covered are all ordinary situations, and the device check
// is what answers next. Only a device failure is ever reported to a human,
// because only a device failure is something they can act on.
//
// Package entitlement — the crypto domain's operations this engine uses: a
// signature check and two digests, asked through pkg/v1 rather than crypto/*
// (ADR 0158 §2). RS256, which the crypto domain has no scheme for, is not here:
// it is oidc.go's, and its doc comment says why it stays local.
//
// Package entitlement — the one error code this IMPLEMENTATION owns and the
// sentinel that carries it, as opposed to the fifteen the CONTRACT declares in
// framework/internal/core/entitlement.
//
// The split is the same one ADR 0079 drew through the domain: core names the
// operator situations every implementation of the port must be able to report —
// revoked, expired, unreachable, no possession — and this range names the
// failures that exist only because this engine has a cache, a network and a
// filesystem. A caller matching on the contract's codes is unaffected by
// anything declared here.
//
// The code and its sentinel share a file rather than the codes.go / errors.go
// pair the larger service packages use: with exactly one of each, splitting
// them puts a two-member group across two files and nothing else in either,
// which is what KTN-STRUCT-PARTITION is for.
//
// Registered in design/sdk.yaml's codes.ranges, which kit writes into
// codeRangeOwners (ADR 0035, ADR 0164), and in //:audit_sources, so a
// code declared here that reaches into another package's range fails the build
// rather than passing quietly.
//
// The Public below is wire-safe and names no path: the public half is the one
// documented safe to put in a response body, and every input this package has
// is a publication endpoint, an untrusted document or a directory on somebody's
// disk. Where it happened lives in Private and in Fields, and diagnose renders
// them for the one place here that is a log rather than a wire.
//
// Package entitlement - refusing a JSON document that names one member twice.
//
// encoding/json accepts {"a":1,"a":2} and keeps the LAST value, silently. The
// roster's signature covers the RAW bytes and is verified before any decoding,
// so this is not a forgery a third party can mount: what it is, is a divergence
// between the party that PUBLISHED a document and the party that READS it. Two
// readers of one byte-identical, correctly signed payload — one keeping the
// first member, one keeping the last — hold different rosters and can both
// prove the vendor signed what they hold.
//
// Refusing is the only reading that cannot contradict anybody. RFC 8725 §2.6
// states the same rule for the JWT case.
//
// It is not pkg/v1/data/codec/strictjson, although ADR 0158 §2 sends JSON there and
// strictjson refuses a duplicate name too. strictjson also refuses every member
// its target does not declare — ADR 0102's one reading, with no mode to relax
// it — and the four documents read here must ignore one: RFC 7517 §4 for a JWK
// member, RFC 7519 §4 for a claim, and a signed roster an older client still
// has to read once the vendor adds a field. Measured: a JWK carrying a member
// its decoding type does not declare is refused MEMBER_UNKNOWN by strictjson.
//
// Package entitlement - the published keys GitHub signs its OIDC tokens with.
//
// Unlike the roster, this set carries no vendor signature: it is authenticated
// by TLS to a fixed host and nothing else. That is weaker, and it is why a
// token verified against it grants only a free CI seat — never a licence. The
// worst a substituted JWKS can do is hand out seats it should not; it cannot
// make an unlicensed machine licensed, because the device path does not
// consult it at all.
//
// Package entitlement - proving a run is CI, rather than taking its word for it.
//
// A CI seat is free, so "am I in CI?" becomes a question worth lying about.
// Every environment variable that answers it — CI, GITHUB_ACTIONS, the absence
// of a TTY, the hostname — is one `export` away from being whatever the caller
// wants, so granting anything on that basis makes the device quota decorative.
//
// GitHub Actions can answer it properly. A workflow granted `id-token: write`
// can exchange two runner-injected values for a JWT signed by GitHub, whose
// claims name the repository and its owner. The request token is an ephemeral
// runner secret: someone outside Actions cannot obtain one, and nobody can
// forge the signature without GitHub's private key.
//
// What this does NOT prove is that the process holding the token is the job it
// was minted for. A token exfiltrated from a legitimate run stays usable off-CI
// until it expires. Closing that needs a server-side nonce, which the offline
// verification model rules out, so the residue is accepted and stated.
//
// Stated as what it IS, which is not what this comment used to claim. "The
// exposure is one free seat" was never demonstrated and is not true. A short
// token bounds the DURATION a stolen proof keeps working; it says nothing about
// the NUMBER of processes that can present it at once. Nothing ties a token to a
// consumer: VerifyActionsToken is pure and offline, it keeps no record of what it
// has already admitted, and two verifiers could not share one if it did. So one
// leaked token satisfies every verifier it reaches, all of them at the same time,
// for as long as it is valid.
//
// The honest bound is ONE FREE SEAT PER VERIFIER FOR UP TO 32 MINUTES:
// maxTokenLifetime (30 min) is the widest window a token may claim, clockSkew
// (2 min) is what checkTiming allows on top when admitting one, and their sum is
// how long after minting a stolen token still verifies. What makes that a bound
// rather than a sentence is ciseat.go handing the token's own expiry to
// coreent.GrantDeadline — without it the GRANT outlived the proof by up to a day,
// and the duration this paragraph names described nothing at all.
//
// Package entitlement — the product: which vendor's roster this binary trusts,
// and the names its artefacts carry.
//
// Package entitlement — parsing and authenticating a published roster.
//
// The signature check lives HERE rather than with the value it produces: core
// declares what a roster IS, and verifying one is a mechanism with a
// cryptographic dependency and a freshness policy.
//
// Package entitlement - Roughtime: an Ed25519-signed statement of the current time,
// bound to a nonce this process just drew.
//
// What it is FOR, stated narrowly because the honest scope is narrow. The clock
// ratchet in cache.go catches a clock moving backwards past vendor-signed
// evidence; it cannot catch a clock that is simply wrong in a direction the
// ratchet never sees, and its resolution is the roster's publication interval.
// A signed timestamp bound to a fresh nonce answers both, and it is the only
// shape that does: an unsigned source (an NTP reply, an HTTPS Date header) is
// forged by anyone on the path, and a signed source without a nonce is replayed
// from a capture.
//
// What it is NOT. It needs the network, so it contributes nothing in the case
// the offline grace window exists for — and an adversary who can substitute an
// origin can also drop UDP to a nonstandard port, which is the entire cost of
// evading it. It is therefore ADVISORY: unreachable or unverifiable means no
// signal and the verification proceeds, because refusing a legitimate user
// whose network blocks port 2002 would trade a real cost for a defence that
// attacker never has to face.
//
// It also never advances the ratchet, and that restriction is load-bearing. A
// response this package accepted in error could otherwise pin the high-water
// mark into the future and refuse the machine permanently. The only thing a
// Roughtime answer can do is refuse a clock that disagrees with it NOW.
//
// # Interoperability is unverified
//
// The constants below are taken from the Roughtime draft, and this client has
// never completed a handshake with a live server: UDP/2002 is filtered on the
// network this was written on, and three independent servers were probed with
// both protocol variants for a total of zero replies. The verifier is tested
// against a server fixture in this package, which proves it is self-consistent
// and NOT that it speaks the same dialect as Cloudflare or Netnod.
//
// RoughtimeServers is empty in committed source for exactly that reason, in the
// same spirit as vendorPublicKeyB64: the mechanism ships, the anchor does not.
// Populating it is a deliberate act by somebody who has watched this client
// verify a real response, and until then every path here is inert.
//
// Package entitlement - verifying a Roughtime answer.
//
// Three signatures and a proof, in an order chosen so nothing downstream runs
// on bytes upstream has not vouched for: the long-term key signs a delegation,
// the delegated key signs the response, the response commits to a Merkle root,
// and the root is shown to contain the nonce this process drew a moment ago.
// Drop any one of the four and the answer becomes replayable, forgeable, or
// about somebody else's request.
//
// Package entitlement - the Roughtime tagged-message encoding.
//
// Split from roughtime.go because it is pure parsing of untrusted bytes and
// nothing else: every function here reads a length or an offset that a hostile
// server chose, so every one of them bounds-checks before it indexes. Keeping
// that apart from the signature checks makes it possible to read either half
// without holding the other in your head.
//
// Package entitlement - orchestration: turn local key material plus a signed
// roster into a grant, or refuse. The fetch is attempted on every cold
// verification and the only thing on disk allowed to stand in for it is a
// bundle this machine already authenticated, read back through the same
// signature and freshness checks — never a decision, always the same document.
//
// Package entitlement - the mandatory-update floor. The roster says which
// version is the lowest allowed to run; this decides whether the binary
// asking is above it.
//
// Package entitlement — the three shapes every error in this package is built
// with, so the choice at a call site is which sentinel rather than which
// spelling.
//
// The split between the first two is whether this package DECIDED the failure
// or was TOLD about one. A decision has no cause to carry — nothing failed, a
// rule was applied to a document — and the sentinel itself is the whole of it.
// A report from outside has a cause that must survive, because
// errors.Is(err, fs.ErrNotExist) and the text the operating system wrote are
// the two things a wrapper most often destroys.
//
// The third, annotate, adds a field to an error whose identity belongs to
// somebody else.
package entitlement
