// Package selfupdate replaces the running binary with a newer signed release.
//
// Package selfupdate replaces the running binary with a newer signed release.
// This file implements SHA-256 integrity verification of downloaded release
// archives against the checksums.txt manifest published by the release
// workflow (CWE-494 mitigation: download of code without integrity check).
//
// INTEGRITY ONLY. Nothing here establishes who published the manifest; that
// is signature.go's job, and matchArchiveDigest must never be reached with a
// manifest verifyArchive has not authenticated first.
//
// Package selfupdate — range 0.3.66.* (ADR 0005 §Registry, allocated in
// codeRangeOwners per ADR 0035).
//
// The domain's own range is 0.2.34.* and it holds every refusal the CONTRACT
// names: an unsigned release, a digest that does not match, a redirect this
// build will not follow. Those are the outcomes a caller of framework/selfupdate
// branches on, and none of them is redeclared here.
//
// What lives in this range is the other half — the failures an IMPLEMENTATION
// has and a contract does not: a JSON body that will not decode, a tar that
// will not open, a temp file that cannot be created next to the running
// binary. The core has no vocabulary for them because a different
// implementation of the same port would fail in different places.
//
// Package selfupdate — consent: whether a caller that did not ask to upgrade
// may nonetheless replace this binary, and the advice printed when it may not.
// Package updater — CONSENT for an upgrade nobody typed.
//
// An explicit `upgrade` command is a deliberate act and needs no permission: a
// it IS the permission. The licence gate is the opposite case. It fires from
// the root command's PersistentPreRun on every invocation, so a plain
// routine command that could reach a code path downloading an archive,
// chmods it 0755, moves it over the running executable and, where sudoers
// allows it, does that last step as root — none of which the person who
// typed `run` asked for. The gate's own comment already conceded the point:
// "this is the one moment a user is staring at a pause they did not ask for".
//
// A mandatory update that simply refuses is useless, so refusing is not what
// this does. It asks when someone is there to answer, takes an explicit
// out-of-band authorisation when nobody is (CI, devcontainers, hooks), and
// when it does decline it prints every way forward rather than leaving a
// dead end.
//
// Package selfupdate — The consent a product declares, and the escalation it forbids (ADR 0150).
// Both are properties of the Service a product builds rather than fields of
// SourceValue: a source says where releases come from, a Service how this
// binary treats them.
//
// Package selfupdate — the half of an error that never goes on a wire, made
// available to the one reader who is entitled to all of it.
//
// errs.Error.Error() renders "[<code> <REASON>] <public>" and deliberately
// nothing else: no private detail, no fields, and not one word from the cause.
// That is right for anything crossing a boundary, and it is exactly wrong for
// the person who just typed `upgrade` and is owed "no space left on device".
//
// So the split is not "throw the detail away", it is "send it somewhere else".
// This file is that somewhere else.
//
// Package selfupdate — privilege escalation: the second, separate opt-in that a
// replacement into a directory this user cannot write requires.
// Package updater — privilege escalation, and the explicit consent it now
// requires.
//
// finalizeReplacement falls back to `sudo -n mv` when the plain rename over
// the running executable is refused for permissions. That fallback used to
// fire unconditionally, which meant an ordinary gated command — a
// command nobody asked to install anything — could end up executing a
// privileged move of a file it had just downloaded. Where sudoers grants
// NOPASSWD (devcontainers, CI images, plenty of laptops) that completes
// silently and writes an attacker-chosen file into a root-owned directory.
//
// Escalation is now opt-in. Not asking is the default, and the refusal names
// the two ways forward so it is never a dead end.
//
// Package selfupdate — the sentinels this IMPLEMENTATION emits, as opposed to
// the ones the domain contract names.
//
// Each var's name equals its errs.Define Reason in SCREAMING_SNAKE form.
//
// Every Public here is wire-safe, so none of them names a path, a URL, a host,
// a tag or an asset. That is not squeamishness: the public half is the one
// documented safe to put in a response body, and this package's inputs are a
// release host and a filesystem — every particular it could name came from one
// of the two. What it says instead is the one thing an operator has to take
// away, which is whether the binary they are running was replaced.
//
// Where it happened and what the filesystem said live in Private and Fields.
// They are not lost to the operator: diagnose renders them and
// ExplainUpgradeFailure prints them under the sentence, because a terminal the
// operator owns is not a wire.
//
// Package selfupdate — the ports, aliased from core.
//
// They are ALIASES rather than a second declaration: a service-local copy of a
// contract the core layer owns compiles fine and drifts silently, and a caller
// holding one of each would find them interchangeable right up until a method
// is added to one of them.
//
// Package selfupdate — the vendor key list and the signature domain of ADR
// 0150: several keys any of which verifies, and a manifest that names its tag
// and its expiry.
//
// Package selfupdate replaces the running binary with a newer signed release.
//
// Package selfupdate — the compile-time proof that osFileSystem still satisfies
// the core FileSystem port.
// Package updater — compile-time assertion for osFileSystem.
//
// Hoisted from osfilesystem.go per KTN-IFACE-ASSERT-PLACEMENT so the
// production source carries no purely verificational declarations.
//
// Package selfupdate — the probe of ADR 0150: the previous binary kept as
// <binary>.prev, the new one asked to answer, and put back when it does not.
//
// Package selfupdate replaces the running binary with a newer signed release.
//
// Package selfupdate — the one platform the replacement step refuses, and why
// the refusal comes before anything is downloaded.
//
// Package selfupdate — authenticity: the detached ed25519 signature over the
// checksum manifest, and the vendor key it is verified against.
// Package updater — release AUTHENTICITY, which is a different property from
// the integrity checksum.go already proves.
//
// checksums.txt establishes that the archive arrived intact. It cannot
// establish who published it: the manifest is fetched from the SAME release,
// on the SAME origin, with the same write credentials as the archive beside
// it (see fetchChecksums — it reuses `downloadURL` verbatim). Stable releases
// are additionally served from a public MIRROR repository, a second
// publishing origin entirely. Anyone able to write assets to that release
// replaces the archive and regenerates the manifest in the same breath, and
// every SHA-256 still matches. The digest defends against a corrupted
// download, never against a hostile publisher.
//
// This file supplies the missing half: a DETACHED ed25519 signature over
// checksums.txt, verified against the vendor public key linked into the
// binary at build time. It is deliberately the same primitive, the same
// algorithm and the same key that pkg/license already uses to authenticate
// the roster (pkg/license/bundle.go ParseBundle, anchored on
// linked in at build time by the consuming binary) — a second scheme would be a second thing
// to get wrong. Once the manifest is authenticated the origin stops
// mattering: a substituted mirror can serve any bytes it likes and cannot
// produce a signature over them.
//
// ABSENT SIGNATURE: FAIL CLOSED, NO TRANSITION WINDOW.
//
// A client that installs an unsigned release when the .sig asset is missing
// has no authenticity check at all — an attacker who can publish assets can
// also decline to publish one, so "accept it when it is absent" is exactly
// equivalent to "never require it". There is no grandfathering window and no
// environment variable that reopens one. The cost is bounded and known: an
// upgrade always moves FORWARD to a release published by the workflow that
// ships with this code, so the only refusals are `upgrade --candidate <tag>`
// against a pre-signing release candidate, which is a developer channel with
// a source checkout one command away.
//
// Package selfupdate — the release source: which project's releases this binary
// updates itself from, and what its artefacts are called.
//
// Package selfupdate replaces the running binary with a newer signed release.
//
// Package selfupdate — the compile-time proof that stdIOCopier still satisfies
// the core Copier port.
// Package updater — compile-time assertion for stdIOCopier.
//
// Hoisted from stdiocopier.go per KTN-IFACE-ASSERT-PLACEMENT so the
// production source carries no purely verificational declarations.
//
// Package selfupdate — the HTTP policy every real Service uses: bounded,
// https-only redirects and a cap on how much of a response is read.
// Package updater — the transport the release endpoints are reached over,
// and the bound on what they are allowed to make this process do.
//
// Everything here treats the GitHub API and the release CDN as untrusted
// input, because they are: stable releases resolve through a PUBLIC mirror
// repository (see repoForTag), which is a second publishing origin, and a
// redirect chain or a response body is chosen entirely by whatever answers.
//
// Package selfupdate replaces the running binary with a newer signed release.
//
// Package selfupdate replaces the running binary with a newer signed release.
// It checks GitHub releases for newer versions and downloads/replaces the binary.
//
// Package selfupdate — the two shapes every error in this package is built
// with, so the choice at a call site is which sentinel rather than which
// spelling.
//
// The split between them is whether this package DECIDED the failure or was
// TOLD about one. A decision has no cause to carry — nothing failed, a rule
// was applied — and the sentinel itself is the whole of it. A report from
// outside has a cause that must survive, because `errors.Is(err, os.ErrPermission)`
// and the text the operating system wrote are the two things a wrapper most
// often destroys.
package selfupdate
