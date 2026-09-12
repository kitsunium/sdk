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
package selfupdate

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	coreupd "github.com/kitsunium/sdk/internal/core/selfupdate"
)

// Signature verification constants.
const (
	// signatureAssetName is the release asset carrying the detached ed25519
	// signature over checksums.txt. Detached rather than embedded so the
	// manifest stays byte-identical to what `sha256sum` emits and the
	// existing installers keep parsing it unchanged.
	signatureAssetName string = checksumsAssetName + ".sig"
	// maxSignatureBytes caps the signature asset read. A base64 ed25519
	// signature is 88 characters and the raw form is 64 bytes; 4KB is
	// defence-in-depth against an endpoint that answers with a page instead.
	maxSignatureBytes int64 = 4 << 10
)

// WithVendorKey pins the ed25519 public half every release must be signed
// with, and returns the Service so construction reads as one expression —
// the same chaining shape license.NewService(...).WithVersion(...) uses.
//
// A Service built WITHOUT this call authenticates nothing and therefore
// installs nothing: verifyArchive refuses with coreupd.NoVendorKey before a single
// byte is fetched. That default is deliberate. The alternative — treat an
// absent key as "verification not required" — would mean any construction
// site that forgot the call silently reverted to the unauthenticated
// behaviour this file exists to end, and nothing would ever fail to point it
// out.
func (u *Service) WithVendorKey(key []byte) *Service {
	//: A nil Service would panic on the field write; returning it unchanged
	//: keeps the chaining expression total.
	if u == nil {
		//: Nothing to configure.
		return nil
	}
	u.vendorKey = ed25519.PublicKey(key)
	//: Return the receiver so the call chains off the constructor.
	return u
}

// verifyArchive proves the buffered archive is the one the vendor published,
// in the ONLY sound order, and returns nil exactly when it is.
//
//  1. The detached signature over checksums.txt is verified against the
//     linked-in vendor key. Nothing the manifest says is believed before
//     this step succeeds.
//  2. The archive's SHA-256 is compared to the entry the now-trusted
//     manifest records for this platform's asset.
//
// Only after both does the caller extract or write anything. Reversing the
// two would let an unauthenticated manifest decide whether an archive is
// acceptable, which is the whole defect; doing either after extraction would
// mean hostile bytes had already been through the tar/zip readers.
func (u *Service) verifyArchive(tag string, archive []byte) error {
	//: Re-checked here even though downloadAndReplace already refused: this
	//: is the function that would otherwise hand a short slice to
	//: ed25519.Verify, and that panics rather than returning false.
	if err := u.canAuthenticate(tag); err != nil {
		//: Bare bubble — canAuthenticate names the tag.
		return err
	}

	// Fetch the manifest and its detached signature from the release.
	manifest, err := u.fetchChecksums(tag)
	//: Propagate manifest download failures (404 → coreupd.ChecksumMissing).
	if err != nil {
		//: Bare bubble — fetchChecksums already wraps with asset+tag context.
		return err
	}
	signature, err := u.fetchSignature(tag)
	//: Propagate signature download failures (404 → coreupd.SignatureMissing).
	if err != nil {
		//: Bare bubble — fetchSignature already wraps with asset+tag context.
		return err
	}

	//: STEP 1 — authenticity. ed25519.Verify over the manifest's exact bytes;
	//: `string(raw)` in fetchChecksums round-trips byte-for-byte, so the
	//: signed payload and the parsed payload are provably the same sequence.
	if !ed25519.Verify(u.vendorKey, []byte(manifest), signature) {
		//: Refuse: whoever produced this manifest is not the vendor.
		return fmt.Errorf("%w: %s for tag %s is not signed by the vendor key linked into this build",
			coreupd.SignatureInvalid, checksumsAssetName, tag)
	}

	//: STEP 2 — integrity, against a manifest that is now trusted.
	return u.matchArchiveDigest(tag, manifest, archive)
}

// canAuthenticate reports whether this build carries a usable vendor key.
//
// It is called at the TOP of downloadAndReplace, before the archive is even
// requested: an unanchored build refusing after a 30MB download would be a
// confusing way to say "this binary cannot install releases". ed25519.Verify
// panics on a key of the wrong length, so the length test is also what keeps
// the verification path total.
func (u *Service) canAuthenticate(tag string) error {
	//: Any length but exactly the ed25519 public size is a build with no
	//: usable anchor — never stamped, or stamped with something broken.
	if len(u.vendorKey) != ed25519.PublicKeySize {
		//: Name the tag so the operator knows which install was refused.
		return fmt.Errorf("%w: cannot authenticate release %s", coreupd.NoVendorKey, tag)
	}
	//: A key this build can verify a release with.
	return nil
}

// fetchSignature downloads checksums.txt.sig from the same release as the
// archive and returns the raw 64-byte ed25519 signature.
//
// A 404 maps to coreupd.SignatureMissing and is a refusal: see the package
// comment for why an unsigned release is not installed.
func (u *Service) fetchSignature(tag string) (signature []byte, fetchErr error) {
	// Build the signature download URL on the same release tag.
	url := fmt.Sprintf(downloadURL, u.src.Owner, u.repoForTag(tag), tag, signatureAssetName)
	// Make HTTP request to download the detached signature.
	resp, err := u.client.Get(url)
	//: Propagate network errors to caller.
	if err != nil {
		//: Wrap with asset+tag context so the failed download is identifiable.
		return nil, fmt.Errorf("%w: downloading %s for tag %s: %w", coreupd.DownloadFailed, signatureAssetName, tag, err)
	}
	defer func() {
		//: Prevent resource leak from unclosed response.
		if cerr := resp.Body.Close(); cerr != nil {
			log.Printf("close response body: %v", cerr)
		}
	}()

	//: A release published without a signature cannot be authenticated.
	if resp.StatusCode == http.StatusNotFound {
		//: Raise the missing sentinel naming the asset and the tag.
		return nil, fmt.Errorf("%w: %s not published for tag %s (an unsigned release is never installed)",
			coreupd.SignatureMissing, signatureAssetName, tag)
	}

	//: Fail fast on any other HTTP error before reading the body.
	if resp.StatusCode != http.StatusOK {
		//: Reuse the download sentinel with status, asset and tag context.
		return nil, fmt.Errorf("%w: status %d fetching %s for tag %s", coreupd.DownloadFailed, resp.StatusCode, signatureAssetName, tag)
	}

	//: Read one byte past the cap so an oversized body is detectable.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxSignatureBytes+1))
	//: Propagate body read failures with asset+tag context.
	if err != nil {
		//: Wrap to identify the signature read phase in operator logs.
		return nil, fmt.Errorf("%w: reading %s for tag %s: %w", coreupd.DownloadFailed, signatureAssetName, tag, err)
	}
	//: A body past the cap is not a signature; an untrusted endpoint must not
	//: choose how much memory we spend.
	if int64(len(raw)) > maxSignatureBytes {
		//: Refuse the oversized asset.
		return nil, fmt.Errorf("%w: %s for tag %s is larger than %d bytes", coreupd.SignatureInvalid, signatureAssetName, tag, maxSignatureBytes)
	}

	signature, decodeErr := decodeSignature(raw)
	//: A published-but-unparseable asset is a refusal like any other; name
	//: the tag so the operator knows which release cannot be authenticated.
	if decodeErr != nil {
		//: Wrap, preserving coreupd.SignatureInvalid for errors.Is.
		return nil, fmt.Errorf("decoding %s for tag %s: %w", signatureAssetName, tag, decodeErr)
	}
	//: A well-formed signature, still entirely unverified.
	return signature, nil
}

// decodeSignature turns the .sig asset's bytes into a raw ed25519 signature.
//
// The canonical published form is base64 (scripts/release/sign-checksums.sh
// emits one line, no wrapping), which is what survives every release tool
// that treats assets as text. A body that is EXACTLY ed25519.SignatureSize
// bytes is taken as the raw binary form instead — that check comes first and
// runs on the untrimmed bytes, because a raw signature can legitimately
// contain a byte that looks like whitespace and trimming it first would
// corrupt one signature in every few hundred.
func decodeSignature(raw []byte) (signature []byte, decodeErr error) {
	//: Raw binary form — matched on exact length before anything is trimmed.
	if len(raw) == ed25519.SignatureSize {
		//: Already the signature; nothing to decode.
		return raw, nil
	}

	text := strings.TrimSpace(string(raw))
	//: Padded then unpadded: `base64 -w0` emits the padded form, but a
	//: hand-produced asset may well drop the "==" and both are unambiguous.
	for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding} {
		decoded, err := encoding.DecodeString(text)
		//: Skip an encoding that does not apply, or that yields the wrong
		//: length — an ed25519 signature is exactly 64 bytes and never other.
		if err != nil || len(decoded) != ed25519.SignatureSize {
			continue
		}
		//: Decoded to a well-formed signature.
		return decoded, nil
	}

	//: Neither form parsed: the asset exists but is not a signature.
	return nil, fmt.Errorf("%w: %s is neither %d raw bytes nor base64 of them (%d bytes read)",
		coreupd.SignatureInvalid, signatureAssetName, ed25519.SignatureSize, len(raw))
}
