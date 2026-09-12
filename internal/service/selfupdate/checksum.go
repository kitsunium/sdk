// Package selfupdate replaces the running binary with a newer signed release.
// This file implements SHA-256 integrity verification of downloaded release
// archives against the checksums.txt manifest published by the release
// workflow (CWE-494 mitigation: download of code without integrity check).
//
// INTEGRITY ONLY. Nothing here establishes who published the manifest; that
// is signature.go's job, and matchArchiveDigest must never be reached with a
// manifest verifyArchive has not authenticated first.
package selfupdate

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	coreupd "github.com/kitsunium/sdk/internal/core/selfupdate"
)

// Checksum verification constants.
const (
	// checksumsAssetName is the release asset holding the sha256sum manifest.
	checksumsAssetName string = "checksums.txt"
	// maxArchiveBytes caps the in-memory archive buffer. Release archives are
	// ~10-30MB today; 256MB leaves ample headroom while bounding memory use
	// against a hostile or corrupted download stream.
	maxArchiveBytes int64 = 256 << 20
	// maxChecksumsBytes caps the checksums.txt manifest read. The real
	// manifest is a few hundred bytes; 1MB is defence-in-depth.
	maxChecksumsBytes int64 = 1 << 20
	// sha256HexLen is the hex-encoded length of a SHA-256 digest: two hex
	// characters per byte, so 64 characters total.
	sha256HexLen int = sha256.Size * 2
)

// bufferArchive reads the whole release archive into memory, refusing
// anything larger than capBytes. Buffering (instead of streaming) is what
// lets the SHA-256 be verified BEFORE any extraction code touches the bytes;
// the zip path already required full buffering (ReaderAt), so this unifies
// both archive shapes at a bounded memory cost.
func bufferArchive(body io.Reader, capBytes int64) (archive []byte, bufErr error) {
	//: Read one byte past the cap so an oversized stream is detectable
	//: without unbounded allocation.
	buf, err := io.ReadAll(io.LimitReader(body, capBytes+1))
	//: Propagate stream read failures with phase context.
	if err != nil {
		//: Wrap to identify the buffering phase in operator logs.
		return nil, fmt.Errorf("buffering release archive: %w", err)
	}
	//: Refuse archives beyond the cap — release archives are ~10-30MB.
	if int64(len(buf)) > capBytes {
		//: Raise the sentinel so callers can errors.Is the size refusal.
		return nil, fmt.Errorf("%w: exceeds %d bytes", coreupd.ArchiveTooLarge, capBytes)
	}
	//: Return the fully buffered archive for hashing then extraction.
	return buf, nil
}

// matchArchiveDigest compares the SHA-256 of the buffered archive bytes
// against the entry manifest records for this platform's asset. Returns
// coreupd.ChecksumMissing when the asset entry is absent, coreupd.ChecksumMismatch when
// the digest diverges. The error message always names the asset and the tag.
//
// It takes the manifest rather than fetching it, and that separation is the
// point: the manifest must already have been AUTHENTICATED by
// Service.verifyArchive before a single one of its claims is acted on. A
// version of this function that fetched its own manifest is exactly what
// shipped the defect — the digest it compared against came from the same
// origin, the same release and the same write credentials as the archive.
func (u *Service) matchArchiveDigest(tag, manifest string, archive []byte) error {
	// Resolve the exact asset name being verified for this platform.
	asset := u.getBinaryName()
	// Locate the digest for the exact archive asset name.
	wantHex, found := findChecksumEntry(manifest, asset)
	//: A manifest without our asset entry is as bad as no manifest at all.
	if !found {
		//: Raise the missing sentinel naming both the asset and the tag.
		return fmt.Errorf("%w: no entry for asset %s in %s (tag %s)", coreupd.ChecksumMissing, asset, checksumsAssetName, tag)
	}
	// Hash the buffered archive bytes before any extraction happens.
	sum := sha256.Sum256(archive)
	gotHex := hex.EncodeToString(sum[:])
	//: Plain (case-insensitive) string equality is sufficient here: both
	//: digests are public data — the manifest is a public release asset and
	//: the archive bytes are attacker-visible — so a timing side-channel has
	//: no secret to leak and crypto/subtle adds nothing.
	if !strings.EqualFold(gotHex, wantHex) {
		//: Refuse the update: the archive does not match its published digest.
		return fmt.Errorf("%w: asset %s (tag %s): manifest %s, downloaded %s", coreupd.ChecksumMismatch, asset, tag, wantHex, gotHex)
	}
	//: Digest verified — the archive may proceed to extraction.
	return nil
}

// fetchChecksums downloads the checksums.txt manifest from the same release
// download URL pattern as the archive itself. A 404 maps to
// coreupd.ChecksumMissing (release published without a manifest); any other
// non-200 maps to coreupd.DownloadFailed.
func (u *Service) fetchChecksums(tag string) (manifest string, fetchErr error) {
	// Build the manifest download URL on the same release tag.
	url := fmt.Sprintf(downloadURL, u.src.Owner, u.repoForTag(tag), tag, checksumsAssetName)
	// Make HTTP request to download the manifest.
	resp, err := u.client.Get(url)
	//: Propagate network errors to caller.
	if err != nil {
		//: Wrap with asset+tag context so the failed download is identifiable.
		return "", fmt.Errorf("%w: downloading %s for tag %s: %w", coreupd.DownloadFailed, checksumsAssetName, tag, err)
	}
	defer func() {
		//: Prevent resource leak from unclosed response.
		if cerr := resp.Body.Close(); cerr != nil {
			log.Printf("close response body: %v", cerr)
		}
	}()

	//: A release without checksums.txt cannot be verified — refuse loudly.
	if resp.StatusCode == http.StatusNotFound {
		//: Raise the missing sentinel naming the manifest asset and the tag.
		return "", fmt.Errorf("%w: %s not published for tag %s", coreupd.ChecksumMissing, checksumsAssetName, tag)
	}

	//: Fail fast on any other HTTP error before reading the body.
	if resp.StatusCode != http.StatusOK {
		//: Reuse the download sentinel with status, asset and tag context.
		return "", fmt.Errorf("%w: status %d fetching %s for tag %s", coreupd.DownloadFailed, resp.StatusCode, checksumsAssetName, tag)
	}

	// Read the manifest body (size-capped defence-in-depth). One byte PAST the
	// cap, like bufferArchive and readCappedAPIBody: reading exactly the cap
	// cannot tell a manifest that fits from one that was truncated, and a
	// truncated manifest fails ed25519 verification — reporting a size problem
	// as a supply-chain signature failure, which is the worst possible advice.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxChecksumsBytes+1))
	//: Propagate body read failures with asset+tag context.
	if err != nil {
		//: Wrap to identify the manifest read phase in operator logs.
		return "", fmt.Errorf("%w: reading %s for tag %s: %w", coreupd.DownloadFailed, checksumsAssetName, tag, err)
	}
	//: Refuse an oversized manifest AS oversized, before anything can mistake
	//: its truncation for a forged signature.
	if int64(len(raw)) > maxChecksumsBytes {
		//: Raise the size sentinel, never the signature one.
		return "", fmt.Errorf("%w: %s for tag %s exceeds %d bytes",
			coreupd.ArchiveTooLarge, checksumsAssetName, tag, maxChecksumsBytes)
	}

	//: Return the raw manifest text for line-by-line parsing.
	return string(raw), nil
}

// findChecksumEntry scans a sha256sum-format manifest ("<hex>  <name>" per
// line) and returns the hex digest recorded for asset. Tolerates one or two
// separator spaces and the binary-mode "*" name marker; lines that do not
// parse as a digest entry are ignored.
func findChecksumEntry(manifest, asset string) (digest string, found bool) {
	//: Walk every manifest line; sha256sum emits one entry per asset.
	for line := range strings.SplitSeq(manifest, "\n") {
		// Split the candidate line into digest and name parts.
		hexPart, namePart, ok := strings.Cut(strings.TrimSpace(line), " ")
		//: Lines without a separator cannot be sha256sum entries — skip.
		if !ok {
			continue
		}
		//: Tolerate the second separator space (text mode) and the leading
		//: "*" marker (binary mode) in front of the file name.
		name := strings.TrimPrefix(strings.TrimSpace(namePart), "*")
		//: Skip unrelated lines (non-digest prefix) and other assets.
		if name != asset || !isHexDigest(hexPart) {
			continue
		}
		//: Found the entry for the exact asset name being downloaded.
		return hexPart, true
	}
	//: Exhausted the manifest without a matching asset entry.
	return "", false
}

// isHexDigest reports whether s is a well-formed hex-encoded SHA-256 digest
// (exactly 64 hex characters, any case).
func isHexDigest(s string) bool {
	//: SHA-256 hex digests are exactly 2*32 characters long.
	if len(s) != sha256HexLen {
		//: Wrong length — cannot be a SHA-256 digest.
		return false
	}
	// Decode validates the character set in one pass.
	_, err := hex.DecodeString(s)
	//: Valid digest iff every character decoded as hex.
	return err == nil
}
