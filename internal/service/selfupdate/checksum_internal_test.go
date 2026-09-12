// Package updater provides self-update functionality for ktn-linter binary.
// White-box tests for the SHA-256 release-archive integrity gate (CWE-494).
package selfupdate

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	coreupd "github.com/kitsunium/sdk/internal/core/selfupdate"
)

// newRealBackedFS returns a mockFileSystem delegating to the real OS,
// anchored at execPath — same shape as the downloadAndReplace scenario tests.
func newRealBackedFS(execPath string) *mockFileSystem {
	//: Delegate every operation to the real OS under the test temp dir.
	return &mockFileSystem{
		executableFunc:   func() (string, error) { return execPath, nil },
		evalSymlinksFunc: func(path string) (string, error) { return path, nil },
		createTempFunc:   os.CreateTemp,
		chmodFunc:        os.Chmod,
		renameFunc:       os.Rename,
		removeFunc:       os.Remove,
	}
}

// TestService_downloadAndReplace_checksumGate is the integration test for the
// release integrity gate: a fake GitHub download endpoint serves the archive
// and a configurable checksums.txt; the update must succeed only when the
// manifest carries the matching digest for the exact asset name.
func TestService_downloadAndReplace_checksumGate(t *testing.T) {
	t.Parallel()

	//: Shared inner-binary payload for every scenario's archive.
	const payload = "verified binary content"

	tests := []struct {
		name string
		//: manifestStatus is the HTTP status served for checksums.txt.
		manifestStatus int
		//: manifestFor builds the checksums.txt body from the archive bytes.
		manifestFor func(t *testing.T, archive []byte) []byte
		//: wantErrIs is the expected sentinel (nil = success).
		wantErrIs error
		//: wantErrContains are substrings the error message must carry.
		wantErrContains []string
	}{
		{
			name:           "valid checksum accepts update",
			manifestStatus: http.StatusOK,
			manifestFor:    checksumsManifestFor,
			wantErrIs:      nil,
		},
		{
			name:           "hash mismatch refuses update",
			manifestStatus: http.StatusOK,
			manifestFor: func(t *testing.T, _ []byte) []byte {
				t.Helper()
				//: Valid 64-hex digest that cannot match any real archive hash.
				return fmt.Appendf(nil, "%s  %s\n", strings.Repeat("0", sha256.Size*2), hostAssetName())
			},
			wantErrIs:       coreupd.ChecksumMismatch,
			wantErrContains: []string{hostAssetName(), "v2.0.0"},
		},
		{
			name:            "missing checksums.txt refuses update",
			manifestStatus:  http.StatusNotFound,
			manifestFor:     func(t *testing.T, _ []byte) []byte { t.Helper(); return []byte("Not Found") },
			wantErrIs:       coreupd.ChecksumMissing,
			wantErrContains: []string{checksumsAssetName, "v2.0.0"},
		},
		{
			name:           "missing asset entry refuses update",
			manifestStatus: http.StatusOK,
			manifestFor: func(t *testing.T, archive []byte) []byte {
				t.Helper()
				//: Correct digest but recorded under a DIFFERENT asset name.
				sum := sha256.Sum256(archive)
				return fmt.Appendf(nil, "%s  ktn-linter_plan9_mips.tar.gz\n", hex.EncodeToString(sum[:]))
			},
			wantErrIs:       coreupd.ChecksumMissing,
			wantErrContains: []string{hostAssetName(), "v2.0.0"},
		},
		{
			name:           "single space and binary marker tolerated",
			manifestStatus: http.StatusOK,
			manifestFor: func(t *testing.T, archive []byte) []byte {
				t.Helper()
				//: sha256sum binary mode emits "<hex> *<name>" — must verify.
				sum := sha256.Sum256(archive)
				return fmt.Appendf(nil, "%s *%s\n", hex.EncodeToString(sum[:]), hostAssetName())
			},
			wantErrIs: nil,
		},
		{
			name:           "unrelated manifest lines ignored",
			manifestStatus: http.StatusOK,
			manifestFor: func(t *testing.T, archive []byte) []byte {
				t.Helper()
				//: Real manifests interleave entries for other assets.
				sum := sha256.Sum256(archive)
				var b bytes.Buffer
				b.WriteString("# release manifest\n")
				b.WriteString("not-a-digest  some-file.txt\n")
				fmt.Fprintf(&b, "%s  ktn-linter_plan9_mips.tar.gz\n", strings.Repeat("a", sha256.Size*2))
				fmt.Fprintf(&b, "%s  %s\n", hex.EncodeToString(sum[:]), hostAssetName())
				return b.Bytes()
			},
			wantErrIs: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Anchor a fake current executable under a temp dir.
			tmpDir := t.TempDir()
			execPath := filepath.Join(tmpDir, testSource.Product)
			//: Seed the "old" binary so refusal paths can prove it untouched.
			if err := os.WriteFile(execPath, []byte("old binary"), 0o755); err != nil {
				t.Fatalf("seed fake executable: %v", err)
			}

			// Build the release archive and its manifest for this scenario.
			// Every manifest here is genuinely vendor-signed: this test is
			// about the DIGEST comparison, so the authenticity half must be
			// satisfied or the checksum branches are never reached at all.
			archive := makeTarGzWithBinary(t, []byte(payload))
			manifest := tc.manifestFor(t, archive)
			vendorPub, vendorPriv := vendorKeypair(t)
			signature := signManifest(vendorPriv, manifest)

			// Fake the GitHub release download endpoints, routed by path.
			server := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				//: The .sig suffix also ends in checksums.txt, so it must be
				//: matched FIRST or the manifest branch swallows it.
				if strings.HasSuffix(r.URL.Path, "/"+signatureAssetName) {
					w.WriteHeader(http.StatusOK)
					if _, werr := w.Write(signature); werr != nil {
						t.Logf("response Write: %v", werr)
					}
					return
				}
				//: checksums.txt path gets the per-scenario manifest response.
				if strings.HasSuffix(r.URL.Path, "/"+checksumsAssetName) {
					w.WriteHeader(tc.manifestStatus)
					if _, werr := w.Write(manifest); werr != nil {
						t.Logf("response Write: %v", werr)
					}
					return
				}
				//: Every other path serves the archive asset itself.
				w.WriteHeader(http.StatusOK)
				if _, werr := w.Write(archive); werr != nil {
					t.Logf("response Write: %v", werr)
				}
			}))
			//: In-memory network: no real port. The server starts on the first
			//: Client() call, which is also what fills server.URL, so start it
			//: here and let the reads below run in any order. NewTestServer
			//: registers the cleanup itself.
			server.Client()

			// Redirect the GitHub download URL to the fake server.
			client := &http.Client{
				Transport: &mockTransport{url: server.URL, client: server.Client()},
			}
			svc := NewUpdaterWithDeps("v1.0.0", testSource, client, newRealBackedFS(execPath), &stdIOCopier{}).
				WithVendorKey(vendorPub)

			err := svc.downloadAndReplace("v2.0.0")

			//: Refusal scenarios: classify the sentinel and prove no replacement.
			if tc.wantErrIs != nil {
				if err == nil {
					t.Fatal("downloadAndReplace() error = nil, want checksum refusal")
				}
				if !errors.Is(err, tc.wantErrIs) {
					t.Errorf("downloadAndReplace() error = %v, want errors.Is %v", err, tc.wantErrIs)
				}
				//: The refusal message must name the asset and the tag.
				for _, sub := range tc.wantErrContains {
					if !strings.Contains(err.Error(), sub) {
						t.Errorf("downloadAndReplace() error = %q, want substring %q", err.Error(), sub)
					}
				}
				//: The on-disk binary must be untouched after a refusal.
				content, rerr := os.ReadFile(execPath)
				if rerr != nil {
					t.Fatalf("read executable after refusal: %v", rerr)
				}
				if string(content) != "old binary" {
					t.Errorf("executable mutated after refusal: %q", content)
				}
				return
			}

			//: Success scenarios: no error and the binary is replaced.
			if err != nil {
				t.Fatalf("downloadAndReplace() unexpected error = %v", err)
			}
			content, rerr := os.ReadFile(execPath)
			if rerr != nil {
				t.Fatalf("read replaced executable: %v", rerr)
			}
			if string(content) != payload {
				t.Errorf("replaced binary = %q, want %q", content, payload)
			}
		})
	}
}

// TestService_fetchChecksums is the canonical KTN-TEST-SYNC test for the
// `fetchChecksums` method on `Service`. Pins the status-code mapping: 200
// returns the body, 404 raises coreupd.ChecksumMissing, other statuses raise
// coreupd.DownloadFailed, and transport failures are wrapped with context.
func TestService_fetchChecksums(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		//: status 0 means "fail the transport" instead of serving a response.
		status          int
		body            string
		wantManifest    string
		wantErrIs       error
		wantErrContains string
	}{
		{
			name:         "ok returns manifest body",
			status:       http.StatusOK,
			body:         "abc  widget_linux_amd64.tar.gz\n",
			wantManifest: "abc  widget_linux_amd64.tar.gz\n",
		},
		{
			name:            "404 raises checksum missing",
			status:          http.StatusNotFound,
			body:            "Not Found",
			wantErrIs:       coreupd.ChecksumMissing,
			wantErrContains: "v9.9.9",
		},
		{
			name:            "500 raises download failed",
			status:          http.StatusInternalServerError,
			body:            "boom",
			wantErrIs:       coreupd.DownloadFailed,
			wantErrContains: checksumsAssetName,
		},
		{
			name:            "transport error wraps context",
			status:          0,
			wantErrContains: "downloading " + checksumsAssetName,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var client Getter
			//: status 0 swaps in a transport that always errors.
			if tc.status == 0 {
				client = &errorGetter{}
			} else {
				server := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(tc.status)
					if _, werr := w.Write([]byte(tc.body)); werr != nil {
						t.Logf("response Write: %v", werr)
					}
				}))
				//: In-memory network: no real port. The server starts on the first
				//: Client() call, which is also what fills server.URL, so start it
				//: here and let the reads below run in any order. NewTestServer
				//: registers the cleanup itself.
				server.Client()
				client = &http.Client{
					Transport: &mockTransport{url: server.URL, client: server.Client()},
				}
			}

			svc := NewUpdaterWithDeps("v1.0.0", testSource, client, &mockFileSystem{}, &mockCopier{})
			manifest, err := svc.fetchChecksums("v9.9.9")

			//: Error rows: classify sentinel and message substrings.
			if tc.wantErrIs != nil || tc.wantErrContains != "" {
				if err == nil {
					t.Fatal("fetchChecksums() error = nil, want error")
				}
				if tc.wantErrIs != nil && !errors.Is(err, tc.wantErrIs) {
					t.Errorf("fetchChecksums() error = %v, want errors.Is %v", err, tc.wantErrIs)
				}
				if tc.wantErrContains != "" && !strings.Contains(err.Error(), tc.wantErrContains) {
					t.Errorf("fetchChecksums() error = %q, want substring %q", err.Error(), tc.wantErrContains)
				}
				return
			}

			//: Happy row: body round-trips verbatim.
			if err != nil {
				t.Fatalf("fetchChecksums() unexpected error = %v", err)
			}
			if manifest != tc.wantManifest {
				t.Errorf("fetchChecksums() = %q, want %q", manifest, tc.wantManifest)
			}
		})
	}
}

// TestService_matchArchiveDigest is the canonical KTN-TEST-SYNC test for
// the `matchArchiveDigest` method on `Service`. Exercises the compare
// step directly: matching digest passes, uppercase digest passes
// (case-insensitive hex), diverging digest raises coreupd.ChecksumMismatch.
//
// It takes the manifest as an argument because that is the whole shape of
// the fix: this function no longer fetches the document it trusts. Its
// authentication is verifyArchive's job and is covered in
// signature_internal_test.go.
func TestService_matchArchiveDigest(t *testing.T) {
	t.Parallel()

	//: Fixed archive bytes shared across rows.
	archive := []byte("archive bytes under test")
	sum := sha256.Sum256(archive)
	goodHex := hex.EncodeToString(sum[:])

	tests := []struct {
		name      string
		digest    string
		wantErrIs error
	}{
		{name: "matching digest passes", digest: goodHex, wantErrIs: nil},
		{name: "uppercase digest passes", digest: strings.ToUpper(goodHex), wantErrIs: nil},
		{name: "diverging digest mismatches", digest: strings.Repeat("f", sha256.Size*2), wantErrIs: coreupd.ChecksumMismatch},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: A manifest carrying the per-row digest for the host asset. No
			//: HTTP is involved: the manifest arrives already authenticated.
			manifest := fmt.Sprintf("%s  %s\n", tc.digest, hostAssetName())
			svc := NewUpdaterWithDeps("v1.0.0", testSource, &mockHTTPClient{}, &mockFileSystem{}, &mockCopier{})

			err := svc.matchArchiveDigest("v2.0.0", manifest, archive)
			//: Mismatch row: classify the sentinel.
			if tc.wantErrIs != nil {
				if !errors.Is(err, tc.wantErrIs) {
					t.Errorf("matchArchiveDigest() error = %v, want errors.Is %v", err, tc.wantErrIs)
				}
				return
			}
			//: Match rows: verification must pass silently.
			if err != nil {
				t.Errorf("matchArchiveDigest() unexpected error = %v", err)
			}
		})
	}
}

// Test_findChecksumEntry pins the sha256sum manifest parsing contract:
// one or two separator spaces, binary-mode "*" marker, unrelated and
// malformed lines ignored, absent asset reported as not found.
func Test_findChecksumEntry(t *testing.T) {
	t.Parallel()

	//: A syntactically valid digest reused across rows.
	digest := strings.Repeat("ab", sha256.Size)

	tests := []struct {
		name      string
		manifest  string
		asset     string
		want      string
		wantFound bool
	}{
		{
			name:      "two spaces text mode",
			manifest:  digest + "  widget_linux_amd64.tar.gz\n",
			asset:     "widget_linux_amd64.tar.gz",
			want:      digest,
			wantFound: true,
		},
		{
			name:      "one space tolerated",
			manifest:  digest + " widget_linux_amd64.tar.gz\n",
			asset:     "widget_linux_amd64.tar.gz",
			want:      digest,
			wantFound: true,
		},
		{
			name:      "binary mode star marker",
			manifest:  digest + " *widget_windows_amd64.zip\n",
			asset:     "widget_windows_amd64.zip",
			want:      digest,
			wantFound: true,
		},
		{
			name:      "unrelated lines ignored",
			manifest:  "# comment\nnot a digest line\n" + digest + "  wanted.tar.gz\n",
			asset:     "wanted.tar.gz",
			want:      digest,
			wantFound: true,
		},
		{
			name:      "short digest rejected",
			manifest:  "abc123  wanted.tar.gz\n",
			asset:     "wanted.tar.gz",
			wantFound: false,
		},
		{
			name:      "non hex digest rejected",
			manifest:  strings.Repeat("zz", sha256.Size) + "  wanted.tar.gz\n",
			asset:     "wanted.tar.gz",
			wantFound: false,
		},
		{
			name:      "asset absent",
			manifest:  digest + "  other.tar.gz\n",
			asset:     "wanted.tar.gz",
			wantFound: false,
		},
		{
			name:      "empty manifest",
			manifest:  "",
			asset:     "wanted.tar.gz",
			wantFound: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, found := findChecksumEntry(tc.manifest, tc.asset)
			//: Presence flag must match the row expectation.
			if found != tc.wantFound {
				t.Fatalf("findChecksumEntry() found = %v, want %v", found, tc.wantFound)
			}
			//: Digest value only meaningful when found.
			if found && got != tc.want {
				t.Errorf("findChecksumEntry() = %q, want %q", got, tc.want)
			}
		})
	}
}

// Test_isHexDigest pins the digest shape validation: exactly 64 hex
// characters in any case, everything else rejected.
func Test_isHexDigest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want bool
	}{
		{name: "lowercase 64 hex", in: strings.Repeat("ab", sha256.Size), want: true},
		{name: "uppercase 64 hex", in: strings.Repeat("AB", sha256.Size), want: true},
		{name: "too short", in: "abc123", want: false},
		{name: "too long", in: strings.Repeat("ab", sha256.Size) + "ff", want: false},
		{name: "non hex characters", in: strings.Repeat("zz", sha256.Size), want: false},
		{name: "empty string", in: "", want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := isHexDigest(tc.in); got != tc.want {
				t.Errorf("isHexDigest(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// Test_bufferArchive pins the size-capped buffering contract: bodies within
// the cap round-trip verbatim, oversized bodies raise coreupd.ArchiveTooLarge,
// and reader failures are wrapped with phase context.
func Test_bufferArchive(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body func() io.Reader
		//: capBytes is the per-row buffering ceiling.
		capBytes        int64
		want            string
		wantErrIs       error
		wantErrContains string
	}{
		{
			name:     "within cap round trips",
			body:     func() io.Reader { return strings.NewReader("payload") },
			capBytes: 64,
			want:     "payload",
		},
		{
			name:     "exactly at cap accepted",
			body:     func() io.Reader { return strings.NewReader("12345678") },
			capBytes: 8,
			want:     "12345678",
		},
		{
			name:      "over cap refused",
			body:      func() io.Reader { return strings.NewReader("123456789") },
			capBytes:  8,
			wantErrIs: coreupd.ArchiveTooLarge,
		},
		{
			name:            "reader failure wrapped",
			body:            func() io.Reader { return io.MultiReader(strings.NewReader("x"), failingReader{}) },
			capBytes:        64,
			wantErrContains: "buffering release archive",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := bufferArchive(tc.body(), tc.capBytes)
			//: Error rows: classify sentinel and message substrings.
			if tc.wantErrIs != nil || tc.wantErrContains != "" {
				if err == nil {
					t.Fatal("bufferArchive() error = nil, want error")
				}
				if tc.wantErrIs != nil && !errors.Is(err, tc.wantErrIs) {
					t.Errorf("bufferArchive() error = %v, want errors.Is %v", err, tc.wantErrIs)
				}
				if tc.wantErrContains != "" && !strings.Contains(err.Error(), tc.wantErrContains) {
					t.Errorf("bufferArchive() error = %q, want substring %q", err.Error(), tc.wantErrContains)
				}
				return
			}
			//: Happy rows: bytes round-trip verbatim.
			if err != nil {
				t.Fatalf("bufferArchive() unexpected error = %v", err)
			}
			if string(got) != tc.want {
				t.Errorf("bufferArchive() = %q, want %q", got, tc.want)
			}
		})
	}
}

// failingReader always errors, exercising the bufferArchive read-error wrap.
type failingReader struct{}

// Read implements io.Reader and always fails.
func (failingReader) Read(_ []byte) (int, error) {
	//: Deterministic failure for the read-error test row.
	return 0, errors.New("simulated read failure")
}
