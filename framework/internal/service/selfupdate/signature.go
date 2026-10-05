package selfupdate

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	coreupd "github.com/kitsunium/sdk/framework/internal/core/selfupdate"
	"github.com/kitsunium/sdk/internal/kernel/errs"
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
	return u.WithVendorKeys(key)
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
	//: is the function that would otherwise verify against no usable key, and
	//: a build that cannot authenticate is told so by name, not as a forgery.
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

	//: STEP 1 — authenticity. An ed25519 verification, through pkg/v1/crypto/sign,
	//: over the manifest's exact bytes; `string(raw)` in fetchChecksums
	//: round-trips byte-for-byte, so the signed payload and the parsed
	//: payload are provably the same sequence.
	//: With a signature domain the signed bytes are the domain, a NUL and the
	//: manifest, and any linked key may have signed them (ADR 0150).
	if !u.verifiedByAnyKey([]byte(u.signedMessage(manifest)), signature) {
		//: Refuse: whoever produced this manifest is not the vendor.
		return refuse(coreupd.SignatureInvalid,
			errs.String("condition", "verify_failed"),
			errs.String("asset", checksumsAssetName),
			errs.String("tag", tag))
	}
	//: A domain also requires the manifest to name this tag and an expiry not
	//: yet past — read only now that the signature vouches for them.
	if err := u.checkStatement(tag, manifest); err != nil {
		//: The statement's own refusal names the condition.
		return err
	}

	//: STEP 2 — integrity, against a manifest that is now trusted.
	return u.matchArchiveDigest(tag, manifest, archive)
}

// canAuthenticate reports whether this build carries a usable vendor key.
//
// It is called at the TOP of downloadAndReplace, before the archive is even
// requested: an unanchored build refusing after a 30MB download would be a
// confusing way to say "this binary cannot install releases". The length test
// is what tells a build with no usable key from a release whose signature does
// not verify.
func (u *Service) canAuthenticate(tag string) error {
	//: A build can verify a release when ONE linked key has the ed25519
	//: public size (ADR 0150): a list of broken keys is no anchor at all.
	for _, k := range u.keys() {
		if len(k) == ed25519.PublicKeySize {
			//: A key this build can verify a release with.
			return nil
		}
	}
	//: Name the tag so the operator knows which install was refused.
	return refuse(coreupd.NoVendorKey,
		errs.String("tag", tag),
		errs.Int("key_bytes", len(u.vendorKey)),
		errs.Int("keys", len(u.vendorKeys)))
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
		return nil, classify(coreupd.DownloadFailed, err,
			errs.String("stage", "get"),
			errs.String("asset", signatureAssetName),
			errs.String("tag", tag))
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
		return nil, refuse(coreupd.SignatureMissing,
			errs.String("asset", signatureAssetName),
			errs.String("tag", tag))
	}

	//: Fail fast on any other HTTP error before reading the body.
	if resp.StatusCode != http.StatusOK {
		//: Reuse the download sentinel with status, asset and tag context.
		return nil, refuse(coreupd.DownloadFailed,
			errs.String("stage", "get"),
			errs.Int("status", resp.StatusCode),
			errs.String("asset", signatureAssetName),
			errs.String("tag", tag))
	}

	//: Read one byte past the cap so an oversized body is detectable.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxSignatureBytes+1))
	//: Propagate body read failures with asset+tag context.
	if err != nil {
		//: Wrap to identify the signature read phase in operator logs.
		return nil, classify(coreupd.DownloadFailed, err,
			errs.String("stage", "read"),
			errs.String("asset", signatureAssetName),
			errs.String("tag", tag))
	}
	//: A body past the cap is not a signature; an untrusted endpoint must not
	//: choose how much memory we spend.
	if int64(len(raw)) > maxSignatureBytes {
		//: Refuse the oversized asset.
		return nil, refuse(coreupd.SignatureInvalid,
			errs.String("condition", "oversized_asset"),
			errs.String("asset", signatureAssetName),
			errs.String("tag", tag),
			errs.Int64("cap_bytes", maxSignatureBytes))
	}

	signature, decodeErr := decodeSignature(raw)
	//: A published-but-unparseable asset is a refusal like any other; name
	//: the tag so the operator knows which release cannot be authenticated.
	if decodeErr != nil {
		//: Origin wins — decodeSignature already raised SignatureInvalid, and
		//: this frame adds only the tag it could not know.
		return nil, classify(coreupd.SignatureInvalid, decodeErr, errs.String("tag", tag))
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
	return nil, refuse(coreupd.SignatureInvalid,
		errs.String("condition", "undecodable_asset"),
		errs.String("asset", signatureAssetName),
		errs.Int("want_bytes", ed25519.SignatureSize),
		errs.Int("got_bytes", len(raw)))
}
