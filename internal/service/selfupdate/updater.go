// Package selfupdate replaces the running binary with a newer signed release.
// It checks GitHub releases for newer versions and downloads/replaces the binary.
package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"golang.org/x/mod/semver"

	coreupd "github.com/kitsunium/sdk/internal/core/selfupdate"
)

// Service constants for GitHub API, versioning, and file operations.
const (
	// apiURL is the GitHub releases API endpoint for latest stable release.
	apiURL string = "https://api.github.com/repos/%s/%s/releases/latest"
	// releasesURL is the GitHub releases API endpoint for all releases.
	releasesURL string = "https://api.github.com/repos/%s/%s/releases"
	// releaseByTagURL is the GitHub API endpoint for a specific release tag.
	releaseByTagURL string = "https://api.github.com/repos/%s/%s/releases/tags/%s"
	// downloadURL is the release asset download URL pattern.
	downloadURL string = "https://github.com/%s/%s/releases/download/%s/%s"
	// httpTimeout is the timeout for HTTP requests.
	httpTimeout time.Duration = 30 * time.Second
	// executablePerm is the permission for executable files.
	executablePerm os.FileMode = 0o755
	// CandidateListSentinel is the sentinel value for --candidate without a tag.
	CandidateListSentinel string = "__list__"
)

// stringFunc is a type for functions returning a string.
type stringFunc = func() string

// Package-level variables for errors and platform detection.
var (
	//: Enable platform-specific binary name selection at download time.
	runtimeGOOS stringFunc = func() string { return runtime.GOOS }
	//: Enable platform-specific binary name selection at download time.
	runtimeGOARCH stringFunc = func() string { return runtime.GOARCH }
)

// Service replaces the running binary with a newer signed release of the
// product its SourceValue names.
// It manages version checking via GitHub API and binary replacement.
// goos/goarch are captured at construction time so the per-platform asset
// resolution (`getBinaryName`, `getArchiveSuffix`, `innerBinaryName`) is
// stable per-instance — tests can construct a literal with explicit values
// instead of mutating package-level `runtimeGOOS`/`runtimeGOARCH` (which
// would race under t.Parallel).
type Service struct {
	version string
	goos    string
	goarch  string
	client  Getter
	fs      FileSystem
	copier  Copier
	// elevate replaces execPath with tmpPath when the plain rename in
	// finalizeReplacement fails with a permission error — the same
	// non-interactive sudo escalation the devcontainer installer scripts
	// use for a root-owned install directory. Swappable so tests can
	// exercise both outcomes without shelling out to a real sudo.
	//
	// The default is guardedSudoMove, NOT sudoMove: escalation requires an
	// explicit opt-in (see elevation.go).
	elevate func(tmpPath, execPath string) error
	// src says which project's releases this binary updates itself from. It
	// carries what the source implementation kept as package constants, which
	// is precisely what made that implementation serve exactly one product.
	src SourceValue
	// vendorKey is the ed25519 public half every release must be signed
	// with, set via WithVendorKey. Empty by default, and an empty key
	// refuses every install rather than skipping verification — see
	// signature.go for why that direction is the only safe one.
	vendorKey ed25519.PublicKey
}

// NewService creates a new updater instance.
//
// The returned Service carries NO vendor key and therefore installs nothing:
// callers chain `.WithVendorKey(...)` with the build's linked-in anchor. That
// is deliberate — see WithVendorKey in signature.go.
func NewService(version string, src SourceValue) *Service {
	//: Initialize updater with the redirect-bounded release HTTP client.
	return &Service{
		version: version,
		src:     src,
		//: Snapshot the host platform once at construction so parallel
		//: tests that mutate runtimeGOOS/GOARCH don't race with this Service.
		goos:   runtimeGOOS(),
		goarch: runtimeGOARCH(),
		//: Not http.DefaultClient's policy: ten hops and an allowed
		//: https->http downgrade are not bounds anyone chose for a path that
		//: ends in chmod 0755 over the running binary. See transport.go.
		client:  newReleaseHTTPClient(),
		fs:      osFileSystem{},
		copier:  stdIOCopier{},
		elevate: src.guardedSudoMove,
	}
}

// NewUpdaterWithDeps creates an updater with custom dependencies for testing.
//
// A nil fs or copier is LEGAL and several callers rely on it: a test that only
// exercises the network path has nothing to inject for the filesystem, and the
// Service never reaches those fields on that path.
//
// This used to call validateDeps and discard its result — a validation whose
// answer nobody read, which is worse than none because the call site reads as a
// guarantee. The function is deleted rather than promoted to a panic or an
// error return: the nil callers are correct, so there is nothing to reject.
func NewUpdaterWithDeps(version string, src SourceValue, client Getter, fs FileSystem, copier Copier) *Service {
	//: Return configured updater instance with injected dependencies
	return &Service{
		version: version,
		src:     src,
		goos:    runtimeGOOS(),
		goarch:  runtimeGOARCH(),
		client:  client,
		fs:      fs,
		copier:  copier,
		elevate: src.guardedSudoMove,
	}
}

// CheckForUpdate checks if an update is available.
func (u *Service) CheckForUpdate() (info UpdateValue, checkErr error) {
	//: Prevent update checks for development builds.
	if u.version == "" || u.version == "dev" {
		//: Return sentinel error to signal dev build.
		return UpdateValue{CurrentVersion: u.version}, coreupd.DevBuild
	}

	// Fetch latest version from GitHub API
	latest, err := u.getLatestVersion()
	//: Propagate API errors to caller.
	if err != nil {
		//: Return error with current version context.
		return UpdateValue{CurrentVersion: u.version}, err
	}

	// Compare versions to determine if update is available
	available := u.isNewer(latest)

	//: Return full result to caller for decision-making.
	return UpdateValue{
		Available:      available,
		CurrentVersion: u.version,
		LatestVersion:  latest,
	}, nil
}

// Upgrade downloads and applies the latest version.
func (u *Service) Upgrade() (info UpdateValue, upgradeErr error) {
	// Check for available updates first
	info, err := u.CheckForUpdate()
	//: Propagate check errors to caller.
	if err != nil {
		//: Return check error without attempting download.
		return info, err
	}

	//: Skip download if no newer version is available.
	if !info.Available {
		//: Return success with current version unchanged.
		return info, nil
	}

	// Download and replace binary with new version
	err = u.downloadAndReplace(info.LatestVersion)
	//: Propagate download errors to caller.
	if err != nil {
		//: Return error from download/replace operation.
		return info, err
	}

	//: Return successful upgrade info.
	return info, nil
}

// repoForTag resolves which distribution repository serves a given tag.
// Prereleases (what rc.yml produces) stay in the private source; everything
// else is a stable release and lives in the public mirror.
//
// The test is the SemVer prerelease component, not a bare hyphen: build
// metadata may legitimately contain one, so `v1.45.2+mirror-build.1` is a
// stable tag that a substring check would misroute to the private repo and
// then 404 on.
func (u *Service) repoForTag(tag string) string {
	//: The prerelease component is the only marker separating the channels.
	if semver.Prerelease(normalizeVersion(tag)) != "" {
		//: Release candidate — private source repository.
		return u.src.candidateRepo()
	}
	//: Stable release — public mirror.
	return u.src.StableRepo
}

// getLatestVersion fetches the latest release version from GitHub.
func (u *Service) getLatestVersion() (latest string, getErr error) {
	// Build API URL for latest release
	url := fmt.Sprintf(apiURL, u.src.Owner, u.src.StableRepo)
	// Make HTTP request to GitHub API
	resp, err := u.client.Get(url)
	//: Propagate network errors to caller.
	if err != nil {
		//: Wrap error to indicate where failure occurred.
		return "", fmt.Errorf("fetching release info: %w", err)
	}
	defer func() {
		//: Prevent resource leak from unclosed response.
		if cerr := resp.Body.Close(); cerr != nil {
			log.Printf("close response body: %v", cerr)
		}
	}()

	//: Fail fast on HTTP errors before parsing.
	if resp.StatusCode != http.StatusOK {
		//: Return error indicating API request failure.
		return "", fmt.Errorf("%w: %d", coreupd.UnexpectedStatus, resp.StatusCode)
	}

	// Parse JSON response under a read cap (an unbounded json.Decoder let
	// the endpoint choose how much memory this process spends).
	var release releaseInfo
	//: Decode response body into release structure.
	if err := decodeJSONBody(resp.Body, maxAPIBodyBytes, &release); err != nil {
		//: Wrap error to indicate parsing failure.
		return "", fmt.Errorf("parsing release info: %w", err)
	}

	//: Return extracted version tag to caller.
	return release.TagName, nil
}

// isNewer checks if the given version is newer than current.
func (u *Service) isNewer(latest string) bool {
	// Normalize both versions for semver comparison
	current := normalizeVersion(u.version)
	remote := normalizeVersion(latest)

	//: Skip comparison if either version is malformed.
	if !semver.IsValid(current) || !semver.IsValid(remote) {
		//: Return false to be conservative (don't upgrade with invalid version).
		return false
	}

	//: Use semver comparison to determine upgrade path.
	return semver.Compare(remote, current) > 0
}

// normalizeVersion ensures a version string has the required "v" prefix.
func normalizeVersion(v string) string {
	//: Ensure version has v prefix for semver compatibility.
	if !strings.HasPrefix(v, "v") {
		//: Add prefix when missing.
		return "v" + v
	}

	//: Return as-is when already prefixed.
	return v
}

// downloadAndReplace downloads the new release archive, AUTHENTICATES it
// (vendor signature over checksums.txt, then the archive's SHA-256 against
// that manifest), extracts the inner binary and atomically replaces the
// current executable.
//
// The update is refused — with nothing extracted and nothing written — when
// authenticity cannot be proven (coreupd.NoVendorKey / coreupd.SignatureMissing /
// coreupd.SignatureInvalid) or integrity cannot (coreupd.ChecksumMissing /
// coreupd.ChecksumMismatch).
func (u *Service) downloadAndReplace(version string) error {
	//: Refuse before spending a download the result can never be used for.
	//: An unanchored build cannot authenticate anything, and finding that
	//: out after 30MB is a poor way to say so.
	if err := u.canAuthenticate(version); err != nil {
		//: Bare bubble — canAuthenticate names the tag.
		return err
	}

	resp, err := u.downloadBinary(version)
	//: Propagate download errors without attempting replacement.
	if err != nil {
		//: Bare bubble — downloadBinary already wraps with context.
		return err
	}
	defer func() {
		//: Prevent resource leak from unclosed response.
		if cerr := resp.Body.Close(); cerr != nil {
			log.Printf("close response body: %v", cerr)
		}
	}()

	execPath, err := u.resolveExecutablePath()
	//: Propagate path resolution errors.
	if err != nil {
		//: Bare bubble — resolveExecutablePath wraps with operation context.
		return err
	}

	// Buffer the whole archive (size-capped) so its SHA-256 can be checked
	// against the release checksums.txt BEFORE any extraction code touches
	// the bytes. Archives are ~10-30MB so the in-memory buffer is acceptable
	// (the zip path already required full buffering for ReaderAt anyway).
	archive, err := bufferArchive(resp.Body, maxArchiveBytes)
	//: Propagate buffering failures (read error or over-cap refusal).
	if err != nil {
		//: Bare bubble — bufferArchive wraps with phase context.
		return err
	}

	//: CWE-494 gate, in order: the vendor's detached signature over
	//: checksums.txt FIRST, then the archive digest against that now-trusted
	//: manifest. Nothing below this line runs on an unauthenticated archive.
	if err := u.verifyArchive(version, archive); err != nil {
		//: Bare bubble — verifyArchive names the asset, the tag and the phase.
		return err
	}

	// Extract the inner binary from the verified archive bytes.
	binReader, cleanup, err := u.openInnerBinary(bytes.NewReader(archive))
	//: Extract pipeline failure — wrap to identify the phase in operator logs.
	if err != nil {
		//: Wrap to identify the extraction phase in the operator log.
		return fmt.Errorf("extracting binary: %w", err)
	}
	defer cleanup()

	//: Stream extracted binary to temp file and atomically replace executable.
	return u.writeAndReplaceBinary(binReader, execPath)
}

// openInnerBinary returns a reader over the product executable contained
// in the downloaded release archive. Returned cleanup must always be called
// (uses no-op when there's nothing to close). The reader is single-use.
func (u *Service) openInnerBinary(body io.Reader) (binary io.Reader, cleanup func(), openErr error) {
	suffix := u.getArchiveSuffix()
	want := u.innerBinaryName()
	//: Two-way archive dispatch — linux/darwin → tar.gz, windows → zip.
	switch suffix {
	//: tar.gz path streams via gzip.NewReader + tar.NewReader.
	case "tar.gz":
		//: Delegate to the streaming opener; caller owns the cleanup.
		return openFromTarGz(body, want)
	//: zip path buffers in memory because archive/zip requires ReaderAt.
	case "zip":
		//: Delegate to the buffered opener; caller owns the cleanup.
		return openFromZip(body, want)
	//: Default — defence-in-depth for an unexpected suffix.
	default:
		//: Defence-in-depth — getArchiveSuffix only emits tar.gz / zip today.
		return nil, func() {}, fmt.Errorf("%w: %s", coreupd.UnknownArchive, suffix)
	}
}

// openFromTarGz walks a gzip-compressed tar stream and returns a reader over
// the first regular file whose basename matches want. The tar Reader is bound
// to the current entry; reading past EntrySize returns io.EOF.
func openFromTarGz(body io.Reader, want string) (binary io.Reader, cleanup func(), openErr error) {
	gz, err := gzip.NewReader(body)
	//: Malformed gzip stream is a hard failure — no partial recovery.
	if err != nil {
		//: Return a no-op cleanup so the caller's `defer cleanup()` is safe.
		return nil, func() {}, fmt.Errorf("opening gzip: %w", err)
	}
	tarReader := tar.NewReader(gz)
	//: Walk every entry until we find a regular file with the wanted basename.
	for {
		hdr, err := tarReader.Next()
		//: io.EOF means the archive is exhausted without a match.
		if errors.Is(err, io.EOF) {
			//: Walked the full archive without finding the inner binary.
			closeBestEffort(gz, "gzip reader")
			//: Raise the sentinel so callers can `errors.Is` for the missing-entry case.
			return nil, func() {}, fmt.Errorf("%w: %s", coreupd.BinaryNotInArchive, want)
		}
		//: Any other error indicates a corrupt tar stream — abort.
		if err != nil {
			//: Corrupt tar stream — abort.
			closeBestEffort(gz, "gzip reader")
			//: Wrap with phase context (`reading tar`) so operators can identify the stage.
			return nil, func() {}, fmt.Errorf("reading tar: %w", err)
		}
		//: Match on basename so nested layouts (e.g. ./tool) still resolve.
		if hdr.Typeflag != tar.TypeReg || filepath.Base(hdr.Name) != want {
			continue
		}
		//: gzip.Reader holds the inflate state; close it after the tar reader is drained.
		return tarReader, func() { closeBestEffort(gz, "gzip reader") }, nil
	}
}

// openFromZip buffers the response body (zip requires ReaderAt) and returns
// a reader over the first file whose basename matches want. Closes the inner
// file reader via the cleanup callback.
func openFromZip(body io.Reader, want string) (binary io.Reader, cleanup func(), openErr error) {
	//: zip.NewReader needs ReaderAt + size; HTTP response is a stream, so buffer.
	buf, err := io.ReadAll(body)
	//: Stream read failure — propagate verbatim.
	if err != nil {
		//: No-op cleanup keeps the caller's defer safe.
		return nil, func() {}, fmt.Errorf("buffering zip body: %w", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf), int64(len(buf)))
	//: Malformed zip — abort.
	if err != nil {
		//: No-op cleanup keeps the caller's defer safe.
		return nil, func() {}, fmt.Errorf("opening zip: %w", err)
	}
	//: Walk every entry until we find one whose basename matches `want`.
	for _, f := range zr.File {
		//: Skip entries whose basename doesn't match — covers nested layouts.
		if filepath.Base(f.Name) != want {
			continue
		}
		rc, err := f.Open()
		//: Opening the entry can fail on corrupt central directory.
		if err != nil {
			//: Wrap with entry name so the operator can identify the bad file.
			return nil, func() {}, fmt.Errorf("opening zip entry %s: %w", f.Name, err)
		}
		//: Cleanup closes the entry reader; ignore the close error (best-effort
		//: since we've already pumped its bytes to the binary temp file).
		return rc, func() { closeBestEffort(rc, "zip entry") }, nil
	}
	//: Walked the whole archive — no matching entry, raise the sentinel.
	return nil, func() {}, fmt.Errorf("%w: %s", coreupd.BinaryNotInArchive, want)
}

// closeBestEffort releases an io.Closer without failing the caller, logging
// the error with the supplied context label so an operator can correlate it
// with the failed cleanup site (zip entry, gzip reader, etc.).
func closeBestEffort(c io.Closer, what string) {
	//: Best-effort Close: log and continue on failure.
	if cerr := c.Close(); cerr != nil {
		log.Printf("close %s: %v", what, cerr)
	}
}

// downloadBinary downloads the binary from GitHub.
func (u *Service) downloadBinary(version string) (resp *http.Response, downloadErr error) {
	// Get platform-specific binary name
	binaryName := u.getBinaryName()
	// Build download URL
	url := fmt.Sprintf(downloadURL, u.src.Owner, u.repoForTag(version), version, binaryName)

	// Make HTTP request to download binary
	resp, err := u.client.Get(url)
	//: Propagate network errors to caller.
	if err != nil {
		//: Wrap error to indicate where failure occurred.
		return nil, fmt.Errorf("downloading binary: %w", err)
	}

	//: Fail fast on HTTP errors before streaming.
	if resp.StatusCode != http.StatusOK {
		//: Clean up response to prevent resource leak.
		if cerr := resp.Body.Close(); cerr != nil {
			log.Printf("close response body: %v", cerr)
		}
		//: Return error indicating download failure.
		return nil, fmt.Errorf("%w: status %d", coreupd.DownloadFailed, resp.StatusCode)
	}

	//: Return response to caller for streaming.
	return resp, nil
}

// resolveExecutablePath gets the current executable path with symlinks resolved.
func (u *Service) resolveExecutablePath() (execPath string, resolveErr error) {
	// Get path to current executable
	execPath, err := u.fs.Executable()
	//: Propagate errors in path retrieval.
	if err != nil {
		//: Wrap error to indicate operation.
		return "", fmt.Errorf("getting executable path: %w", err)
	}

	// Resolve any symlinks in path
	execPath, err = u.fs.EvalSymlinks(execPath)
	//: Propagate errors in symlink resolution.
	if err != nil {
		//: Wrap error to indicate operation.
		return "", fmt.Errorf("resolving symlinks: %w", err)
	}

	//: Return the real executable path for replacement.
	return execPath, nil
}

// writeAndReplaceBinary writes content to temp file and replaces executable.
func (u *Service) writeAndReplaceBinary(content io.Reader, execPath string) (err error) {
	// Stage the download next to the target so the common case (a
	// user-writable install dir) gets a same-filesystem, atomic rename with
	// no elevation involved.
	tmpFile, err := u.fs.CreateTemp(filepath.Dir(execPath), u.src.Product+"-update-*")
	//: A directory the current user can't write into at all — most often a
	//: root-owned /usr/local/bin install — leaves nowhere local to stage the
	//: file. Fall back to the OS temp dir; finalizeReplacement's sudo -n
	//: retry performs the actual install move.
	if errors.Is(err, os.ErrPermission) {
		tmpFile, err = u.fs.CreateTemp("", u.src.Product+"-update-*")
	}
	//: Propagate file creation errors.
	if err != nil {
		//: Wrap error to indicate operation.
		return fmt.Errorf("creating temp file: %w", err)
	}
	defer func() {
		err = errors.Join(err, tmpFile.Close())
	}()
	tmpPath := tmpFile.Name()

	// Write content to temp file
	err = u.writeTempFileContent(tmpFile, content)
	//: Clean up temp file if write fails.
	if err != nil {
		u.fs.Remove(tmpPath)
		//: Return write error.
		return err
	}

	//: Proceed with atomic replacement of executable.
	return u.finalizeReplacement(tmpPath, execPath)
}

// writeTempFileContent writes content to a file writer.
func (u *Service) writeTempFileContent(writer io.Writer, content io.Reader) error {
	// Copy downloaded content to temp file
	_, err := u.copier.Copy(writer, content)
	//: Propagate stream copy errors.
	if err != nil {
		//: Wrap error to indicate operation.
		return fmt.Errorf("writing temp file: %w", err)
	}

	//: Return success indicator.
	return nil
}

// finalizeReplacement sets permissions and replaces the binary.
func (u *Service) finalizeReplacement(tmpPath, execPath string) error {
	// Set executable permissions on temp file
	err := u.fs.Chmod(tmpPath, executablePerm)
	//: Propagate chmod errors and clean up on failure.
	if err != nil {
		u.fs.Remove(tmpPath)
		//: Wrap error to indicate operation.
		return fmt.Errorf("setting permissions: %w", err)
	}

	// Atomically replace old binary with new one
	err = u.fs.Rename(tmpPath, execPath)
	//: A clean rename is the common case — nothing left to elevate.
	if err == nil {
		//: Return success indicator.
		return nil
	}
	//: Anything other than a permission failure (missing path, cross-device
	//: link, disk full, ...) won't be fixed by retrying as another user.
	if !errors.Is(err, os.ErrPermission) {
		u.fs.Remove(tmpPath)
		//: Wrap error to indicate operation.
		return fmt.Errorf("replacing binary: %w", err)
	}
	//: The install directory needs elevated rights — retry once via
	//: non-interactive sudo (see Service.elevate) before giving up, so an
	//: upgrade run by the owning user doesn't have to be re-invoked by hand.
	if elevateErr := u.elevate(tmpPath, execPath); elevateErr != nil {
		u.fs.Remove(tmpPath)
		//: Name both failures: the original refusal and why the escalated
		//: retry didn't save it. BOTH are wrapped with %w, not just the
		//: first: the caller branches on coreupd.ElevationNotAuthorised to print the
		//: one opt-in that would unblock the install, and a %v here would
		//: make that sentinel invisible to errors.Is.
		return fmt.Errorf("replacing binary: %w (retried with sudo -n: %w)", err, elevateErr)
	}

	//: Return success indicator.
	return nil
}

// getBinaryName returns the release asset name for the platform captured
// at construction time. Releases use goreleaser-style packaging:
// `<product>_<goos>_<goarch>.tar.gz` on linux/darwin and `.zip` on
// windows. The same archive name is consumed by the devcontainer feature
// install.sh so the project ships a single asset shape (snake_case +
// archive) across all consumers.
func (u *Service) getBinaryName() string {
	//: Snake-case + per-suffix matches the goreleaser convention end-to-end.
	return fmt.Sprintf("%s_%s_%s.%s", u.src.Product, u.goos, u.goarch, u.getArchiveSuffix())
}

// getArchiveSuffix returns the archive format for the platform captured at
// construction. Stored as a separate accessor so openInnerBinary can
// pick the right reader (tar/gzip vs zip) from a single source of truth.
func (u *Service) getArchiveSuffix() string {
	//: Windows ships .zip; Linux/Darwin ship .tar.gz — goreleaser default.
	if u.goos == "windows" {
		//: Windows-only branch: zip is the native archive format on that OS.
		return "zip"
	}
	//: Linux/Darwin default — gzip+tar (goreleaser standard).
	return "tar.gz"
}

// innerBinaryName returns the file name expected INSIDE the release archive.
// goreleaser packs the bare binary at the archive root, so this is just the
// product name (with `.exe` on windows).
func (u *Service) innerBinaryName() string {
	//: Windows binaries carry the `.exe` extension by Go toolchain convention.
	if u.goos == "windows" {
		//: Windows-only branch.
		return u.src.Product + ".exe"
	}
	//: Linux/Darwin — bare executable name.
	return u.src.Product
}
