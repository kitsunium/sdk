// Package updater provides self-update functionality for ktn-linter binary.
// White-box tests for the release AUTHENTICITY gate: the detached ed25519
// signature over checksums.txt, and the order it is verified in relative to
// the SHA-256 comparison and to any extraction or write.
//
// Every key here is generated inside the test that uses it. Nothing in this
// repository holds key material, and a fixture constant would be exactly
// that.
package selfupdate

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	coreupd "github.com/kitsunium/sdk/internal/core/selfupdate"
)

// vendorKeypair returns a throwaway ed25519 keypair for a single test.
func vendorKeypair(t *testing.T) (public ed25519.PublicKey, private ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	//: A keypair we could not generate makes the whole scenario meaningless.
	if err != nil {
		t.Fatalf("generate ed25519 keypair: %v", err)
	}
	return pub, priv
}

// releaseFixture is a complete, WELL-FORMED release: the archive, the
// sha256sum manifest covering it under this platform's asset name, and the
// vendor's detached signature over that manifest's exact bytes.
//
// Tests perturb exactly one of the three and assert the refusal, which is
// what makes each scenario name the property it pins rather than "something
// was wrong somewhere".
type releaseFixture struct {
	// pub is the public half a Service must be given to accept this release.
	pub ed25519.PublicKey
	// priv is kept so a test can re-sign a manifest it modified.
	priv ed25519.PrivateKey
	// archive is the tar.gz release asset bytes.
	archive []byte
	// manifest is the checksums.txt body, signature payload included.
	manifest []byte
	// sig is the base64 form published as checksums.txt.sig.
	sig []byte
}

// newReleaseFixture builds a signed release around an inner-binary payload.
func newReleaseFixture(t *testing.T, payload []byte) releaseFixture {
	t.Helper()
	pub, priv := vendorKeypair(t)
	archive := makeTarGzWithBinary(t, payload)
	manifest := checksumsManifestFor(t, archive)
	return releaseFixture{
		pub:      pub,
		priv:     priv,
		archive:  archive,
		manifest: manifest,
		sig:      signManifest(priv, manifest),
	}
}

// signManifest returns the published .sig body for a manifest: the detached
// ed25519 signature, base64, exactly as scripts/release/sign-checksums.sh
// emits it.
func signManifest(priv ed25519.PrivateKey, manifest []byte) []byte {
	return []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(priv, manifest)))
}

// serveAsset writes whichever release asset `path` names and reports whether
// it handled the request, so a scenario's handler can delegate the two assets
// it is NOT perturbing and override only the one it is.
func (f releaseFixture) serveAsset(t *testing.T, w http.ResponseWriter, path string) bool {
	t.Helper()
	//: Dispatch based on the variant to apply the correct logic.
	switch {
	//: The signature asset ends in .sig, which also ends in checksums.txt —
	//: so it MUST be matched before the manifest or it never resolves.
	case strings.HasSuffix(path, "/"+signatureAssetName):
		writeBody(t, w, f.sig)
	//: The sha256sum manifest.
	case strings.HasSuffix(path, "/"+checksumsAssetName):
		writeBody(t, w, f.manifest)
	//: The release archive itself.
	case strings.Contains(path, "/releases/download/"):
		writeBody(t, w, f.archive)
	//: Anything else is not ours to answer.
	default:
		return false
	}
	return true
}

// writeBody writes a 200 with body, logging a write failure rather than
// failing the test from the server goroutine.
func writeBody(t *testing.T, w http.ResponseWriter, body []byte) {
	t.Helper()
	w.WriteHeader(http.StatusOK)
	//: httptest's writer cannot fail under contract; log so a future custom
	//: recorder makes the breakage observable instead of silent.
	if _, err := w.Write(body); err != nil {
		t.Logf("response Write: %v", err)
	}
}

// signedServiceFor stands up a fake release origin serving `handler` and
// returns a Service wired to it, pinned to `key`.
func signedServiceFor(t *testing.T, key ed25519.PublicKey, fs FileSystem, handler http.HandlerFunc) *Service {
	t.Helper()
	server := httptest.NewTestServer(t, handler)
	//: In-memory network: no real port. The server starts on the first
	//: Client() call, which is also what fills server.URL, so start it
	//: here and let the reads below run in any order. NewTestServer
	//: registers the cleanup itself.
	server.Client()
	client := &http.Client{Transport: &mockTransport{url: server.URL, client: server.Client()}}
	return NewUpdaterWithDeps("v1.0.0", testSource, client, fs, &stdIOCopier{}).WithVendorKey(key)
}

// dirEntries returns the sorted names of everything in dir. Used to prove a
// refusal wrote NOTHING — not the replaced binary, and not the staging temp
// file writeAndReplaceBinary creates next to it.
func dirEntries(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	//: A directory we cannot list makes the assertion vacuous.
	if err != nil {
		t.Fatalf("read dir %s: %v", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	slices.Sort(names)
	return names
}

// TestService_verifyArchive is the canonical KTN-TEST-SYNC test for the
// `verifyArchive` method on `Service`. It pins the authenticity contract one
// perturbation at a time: a correct release verifies, and each of the four
// ways it can be wrong raises its own sentinel.
func TestService_verifyArchive(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		//: mutate perturbs the otherwise-correct fixture. nil = leave it valid.
		mutate func(t *testing.T, f *releaseFixture)
		//: sigStatus overrides the .sig response status (0 = serve it normally).
		sigStatus int
		//: withoutKey builds the Service with no vendor key at all.
		withoutKey bool
		wantErrIs  error
	}{
		{
			name:      "correct release verifies",
			wantErrIs: nil,
		},
		{
			name: "signature from another key refuses",
			mutate: func(t *testing.T, f *releaseFixture) {
				t.Helper()
				//: A well-formed signature over the exact right bytes — by
				//: somebody who is not the vendor. This is the mirror-takeover
				//: case: the attacker can regenerate the manifest freely and
				//: still cannot produce this one value.
				_, impostor := vendorKeypair(t)
				f.sig = signManifest(impostor, f.manifest)
			},
			wantErrIs: coreupd.SignatureInvalid,
		},
		{
			name: "manifest tampered after signing refuses",
			mutate: func(t *testing.T, f *releaseFixture) {
				t.Helper()
				//: Keep the vendor's real signature; swap the digest under it
				//: for one matching an archive the attacker controls.
				f.manifest = []byte(strings.Repeat("0", sha256HexLen) + "  " + hostAssetName() + "\n")
			},
			wantErrIs: coreupd.SignatureInvalid,
		},
		{
			name: "signed manifest that does not match the archive refuses",
			mutate: func(t *testing.T, f *releaseFixture) {
				t.Helper()
				//: Genuinely vendor-signed, and genuinely about a different
				//: archive — a stale or mis-published manifest.
				f.manifest = []byte(strings.Repeat("a", sha256HexLen) + "  " + hostAssetName() + "\n")
				f.sig = signManifest(f.priv, f.manifest)
			},
			wantErrIs: coreupd.ChecksumMismatch,
		},
		{
			name:      "release published without a signature refuses",
			sigStatus: http.StatusNotFound,
			wantErrIs: coreupd.SignatureMissing,
		},
		{
			name: "unparseable signature asset refuses",
			mutate: func(t *testing.T, f *releaseFixture) {
				t.Helper()
				f.sig = []byte("<!doctype html><html>not a signature</html>")
			},
			wantErrIs: coreupd.SignatureInvalid,
		},
		{
			name:       "build with no vendor key installs nothing",
			withoutKey: true,
			wantErrIs:  coreupd.NoVendorKey,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture := newReleaseFixture(t, []byte("authentic binary"))
			//: Apply this scenario's single perturbation.
			if tc.mutate != nil {
				tc.mutate(t, &fixture)
			}

			key := fixture.pub
			//: The unanchored-build row passes no key at all.
			if tc.withoutKey {
				key = nil
			}

			svc := signedServiceFor(t, key, &mockFileSystem{}, func(w http.ResponseWriter, r *http.Request) {
				//: Override the signature response when the row asks for it.
				if tc.sigStatus != 0 && strings.HasSuffix(r.URL.Path, "/"+signatureAssetName) {
					w.WriteHeader(tc.sigStatus)
					return
				}
				//: Otherwise serve the fixture's own assets.
				if !fixture.serveAsset(t, w, r.URL.Path) {
					w.WriteHeader(http.StatusNotFound)
				}
			})

			err := svc.verifyArchive("v2.0.0", fixture.archive)

			//: Refusal rows: the sentinel must classify, and the message must
			//: name the tag so an operator knows which install was refused.
			if tc.wantErrIs != nil {
				if !errors.Is(err, tc.wantErrIs) {
					t.Fatalf("verifyArchive() error = %v, want errors.Is %v", err, tc.wantErrIs)
				}
				if !strings.Contains(err.Error(), "v2.0.0") {
					t.Errorf("verifyArchive() error = %q, want it to name the tag", err.Error())
				}
				return
			}
			//: Success row: an authentic release verifies silently.
			if err != nil {
				t.Errorf("verifyArchive() unexpected error = %v", err)
			}
		})
	}
}

// TestService_verifyArchive_signatureBeforeChecksum pins the ORDER, which the
// per-sentinel table above cannot: it constructs a release where BOTH checks
// would fail and requires the signature refusal to be the one reported.
//
// Without the ordering this test passes trivially, so it is paired with the
// inverse: a correctly signed manifest whose digest is wrong must reach the
// checksum comparison and report THAT. Together they pin one order and reject
// the other.
func TestService_verifyArchive_signatureBeforeChecksum(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		//: mutate breaks one or both halves of the fixture.
		mutate    func(t *testing.T, f *releaseFixture)
		wantErrIs error
	}{
		{
			name: "both broken reports the signature",
			mutate: func(t *testing.T, f *releaseFixture) {
				t.Helper()
				//: Digest is wrong AND the signature is not the vendor's. If
				//: the checksum ran first this would report coreupd.ChecksumMismatch.
				f.manifest = []byte(strings.Repeat("b", sha256HexLen) + "  " + hostAssetName() + "\n")
				_, impostor := vendorKeypair(t)
				f.sig = signManifest(impostor, f.manifest)
			},
			wantErrIs: coreupd.SignatureInvalid,
		},
		{
			name: "signature valid and digest wrong reports the checksum",
			mutate: func(t *testing.T, f *releaseFixture) {
				t.Helper()
				f.manifest = []byte(strings.Repeat("b", sha256HexLen) + "  " + hostAssetName() + "\n")
				f.sig = signManifest(f.priv, f.manifest)
			},
			wantErrIs: coreupd.ChecksumMismatch,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture := newReleaseFixture(t, []byte("authentic binary"))
			tc.mutate(t, &fixture)

			svc := signedServiceFor(t, fixture.pub, &mockFileSystem{}, func(w http.ResponseWriter, r *http.Request) {
				if !fixture.serveAsset(t, w, r.URL.Path) {
					w.WriteHeader(http.StatusNotFound)
				}
			})

			err := svc.verifyArchive("v2.0.0", fixture.archive)
			//: The reported sentinel IS the ordering evidence.
			if !errors.Is(err, tc.wantErrIs) {
				t.Errorf("verifyArchive() error = %v, want errors.Is %v", err, tc.wantErrIs)
			}
		})
	}
}

// TestService_downloadAndReplace_authenticityGate is the end-to-end proof
// that verification happens BEFORE anything reaches the disk.
//
// It runs the real replacement path against a real temp directory and, on
// every refusal row, asserts two things: the installed binary is byte-for-byte
// unchanged, and the directory holds exactly the entries it held before. The
// second is what rules out extraction — writeAndReplaceBinary stages the
// extracted bytes in a `widget-update-*` temp file NEXT TO the executable,
// so a single new entry there is direct evidence that hostile bytes went
// through the tar reader and onto the filesystem.
func TestService_downloadAndReplace_authenticityGate(t *testing.T) {
	t.Parallel()

	const oldBinary = "old binary"
	const newBinary = "new binary content"

	tests := []struct {
		name string
		//: mutate perturbs the otherwise-correct release. nil = leave valid.
		mutate func(t *testing.T, f *releaseFixture)
		//: sigStatus overrides the .sig response status (0 = serve normally).
		sigStatus int
		//: withoutKey builds the Service with no vendor key.
		withoutKey bool
		wantErrIs  error
	}{
		{name: "authentic release is installed", wantErrIs: nil},
		{
			name: "forged signature installs nothing",
			mutate: func(t *testing.T, f *releaseFixture) {
				t.Helper()
				_, impostor := vendorKeypair(t)
				f.sig = signManifest(impostor, f.manifest)
			},
			wantErrIs: coreupd.SignatureInvalid,
		},
		{
			name: "manifest tampered after signing installs nothing",
			mutate: func(t *testing.T, f *releaseFixture) {
				t.Helper()
				f.manifest = append(f.manifest, "# appended after signing\n"...)
			},
			wantErrIs: coreupd.SignatureInvalid,
		},
		{
			name:      "unsigned release installs nothing",
			sigStatus: http.StatusNotFound,
			wantErrIs: coreupd.SignatureMissing,
		},
		{
			name:       "unanchored build installs nothing",
			withoutKey: true,
			wantErrIs:  coreupd.NoVendorKey,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tmpDir := t.TempDir()
			execPath := filepath.Join(tmpDir, testSource.Product)
			//: Seed the "currently installed" binary so a refusal can be
			//: shown to have left it alone.
			if err := os.WriteFile(execPath, []byte(oldBinary), 0o755); err != nil {
				t.Fatalf("seed fake executable: %v", err)
			}
			before := dirEntries(t, tmpDir)

			fixture := newReleaseFixture(t, []byte(newBinary))
			//: Apply this scenario's single perturbation.
			if tc.mutate != nil {
				tc.mutate(t, &fixture)
			}
			key := fixture.pub
			//: The unanchored-build row passes no key at all.
			if tc.withoutKey {
				key = nil
			}

			svc := signedServiceFor(t, key, newRealBackedFS(execPath), func(w http.ResponseWriter, r *http.Request) {
				if tc.sigStatus != 0 && strings.HasSuffix(r.URL.Path, "/"+signatureAssetName) {
					w.WriteHeader(tc.sigStatus)
					return
				}
				if !fixture.serveAsset(t, w, r.URL.Path) {
					w.WriteHeader(http.StatusNotFound)
				}
			})

			err := svc.downloadAndReplace("v2.0.0")

			//: Refusal rows: nothing installed, nothing extracted, nothing
			//: written anywhere near the executable.
			if tc.wantErrIs != nil {
				if !errors.Is(err, tc.wantErrIs) {
					t.Fatalf("downloadAndReplace() error = %v, want errors.Is %v", err, tc.wantErrIs)
				}
				content, rerr := os.ReadFile(execPath)
				if rerr != nil {
					t.Fatalf("read executable after refusal: %v", rerr)
				}
				if string(content) != oldBinary {
					t.Errorf("executable mutated after refusal: %q, want %q", content, oldBinary)
				}
				if after := dirEntries(t, tmpDir); !slices.Equal(after, before) {
					t.Errorf("refusal wrote to disk: dir entries %v, want %v — verification ran after extraction", after, before)
				}
				return
			}

			//: Success row: the authentic payload is now the executable.
			if err != nil {
				t.Fatalf("downloadAndReplace() unexpected error = %v", err)
			}
			content, rerr := os.ReadFile(execPath)
			if rerr != nil {
				t.Fatalf("read replaced executable: %v", rerr)
			}
			if string(content) != newBinary {
				t.Errorf("replaced binary = %q, want %q", content, newBinary)
			}
		})
	}
}

// TestService_fetchSignature pins the status-code mapping for the signature
// asset: http.StatusOK returns the decoded signature, http.StatusNotFound
// raises coreupd.SignatureMissing, any other status raises coreupd.DownloadFailed, an
// oversized body is refused without being decoded, and a transport failure is
// wrapped with context.
func TestService_fetchSignature(t *testing.T) {
	t.Parallel()

	_, priv := vendorKeypair(t)
	valid := signManifest(priv, []byte("manifest bytes"))

	tests := []struct {
		name string
		//: status 0 means "fail the transport" instead of serving a response.
		status          int
		body            []byte
		wantErrIs       error
		wantErrContains string
	}{
		{name: "ok returns the decoded signature", status: http.StatusOK, body: valid},
		{
			name:            "404 raises signature missing",
			status:          http.StatusNotFound,
			body:            []byte("Not Found"),
			wantErrIs:       coreupd.SignatureMissing,
			wantErrContains: signatureAssetName,
		},
		{
			name:            "500 raises download failed",
			status:          http.StatusInternalServerError,
			body:            []byte("boom"),
			wantErrIs:       coreupd.DownloadFailed,
			wantErrContains: signatureAssetName,
		},
		{
			name:      "oversized body refused",
			status:    http.StatusOK,
			body:      []byte(strings.Repeat("A", int(maxSignatureBytes)+1)),
			wantErrIs: coreupd.SignatureInvalid,
			//: The refusal must be about the size, not a decode failure.
			wantErrContains: "larger than",
		},
		{
			name:            "transport error wraps context",
			status:          0,
			wantErrContains: "downloading " + signatureAssetName,
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
					if _, err := w.Write(tc.body); err != nil {
						t.Logf("response Write: %v", err)
					}
				}))
				//: In-memory network: no real port. The server starts on the first
				//: Client() call, which is also what fills server.URL, so start it
				//: here and let the reads below run in any order. NewTestServer
				//: registers the cleanup itself.
				server.Client()
				client = &http.Client{Transport: &mockTransport{url: server.URL, client: server.Client()}}
			}

			svc := NewUpdaterWithDeps("v1.0.0", testSource, client, &mockFileSystem{}, &mockCopier{})
			signature, err := svc.fetchSignature("v9.9.9")

			//: Error rows: classify the sentinel and the message substring.
			if tc.wantErrIs != nil || tc.wantErrContains != "" {
				if err == nil {
					t.Fatal("fetchSignature() error = nil, want error")
				}
				if tc.wantErrIs != nil && !errors.Is(err, tc.wantErrIs) {
					t.Errorf("fetchSignature() error = %v, want errors.Is %v", err, tc.wantErrIs)
				}
				if !strings.Contains(err.Error(), tc.wantErrContains) {
					t.Errorf("fetchSignature() error = %q, want substring %q", err.Error(), tc.wantErrContains)
				}
				return
			}

			//: Happy row: the base64 body decoded back to the raw signature.
			if err != nil {
				t.Fatalf("fetchSignature() unexpected error = %v", err)
			}
			if len(signature) != ed25519.SignatureSize {
				t.Errorf("fetchSignature() len = %d, want %d", len(signature), ed25519.SignatureSize)
			}
		})
	}
}

// Test_decodeSignature pins the two accepted published forms and rejects
// everything else. The raw-bytes row is the one that matters most: it must be
// matched on length BEFORE any whitespace trimming, or a signature whose last
// byte happens to be 0x0a is silently corrupted.
func Test_decodeSignature(t *testing.T) {
	t.Parallel()

	_, priv := vendorKeypair(t)
	raw := ed25519.Sign(priv, []byte("payload"))

	tests := []struct {
		name      string
		body      []byte
		want      []byte
		wantErrIs error
	}{
		{name: "raw 64 bytes", body: raw, want: raw},
		{name: "padded base64", body: []byte(base64.StdEncoding.EncodeToString(raw)), want: raw},
		{name: "unpadded base64", body: []byte(base64.RawStdEncoding.EncodeToString(raw)), want: raw},
		{name: "base64 with trailing newline", body: []byte(base64.StdEncoding.EncodeToString(raw) + "\n"), want: raw},
		{name: "empty body", body: nil, wantErrIs: coreupd.SignatureInvalid},
		{name: "html error page", body: []byte("<html>404</html>"), wantErrIs: coreupd.SignatureInvalid},
		{
			name: "base64 of the wrong length",
			//: Well-formed base64 that decodes to 32 bytes, not 64.
			body:      []byte(base64.StdEncoding.EncodeToString(raw[:32])),
			wantErrIs: coreupd.SignatureInvalid,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := decodeSignature(tc.body)
			//: Refusal rows: classify the sentinel and return no signature.
			if tc.wantErrIs != nil {
				if !errors.Is(err, tc.wantErrIs) {
					t.Fatalf("decodeSignature() error = %v, want errors.Is %v", err, tc.wantErrIs)
				}
				if got != nil {
					t.Errorf("decodeSignature() = %x, want nil on refusal", got)
				}
				return
			}
			//: Accepted rows: the exact signature bytes come back.
			if err != nil {
				t.Fatalf("decodeSignature() unexpected error = %v", err)
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("decodeSignature() = %x, want %x", got, tc.want)
			}
		})
	}
}

// Test_decodeSignature_rawByteWithNewlineTail is the regression that pins the
// "length check before trim" ordering with a value the trimming order could
// not have produced: a raw signature whose final byte IS 0x0a.
//
// The control row is not padding. Trimming first is only WRONG for signatures
// ending in whitespace — every other raw signature survives either order — so
// a single accepted signature proves nothing about the ordering. The pairing
// is the proof: both must be accepted verbatim, and only the newline-tailed
// one can distinguish the two implementations.
func Test_decodeSignature_rawByteWithNewlineTail(t *testing.T) {
	t.Parallel()

	_, priv := vendorKeypair(t)

	//: Search for a real signature ending in '\n' rather than fabricating a
	//: 64-byte blob: this is exactly the shape the release pipeline can emit.
	var newlineTailed []byte
	for i := range 4096 {
		candidate := ed25519.Sign(priv, []byte{byte(i), byte(i >> 8)})
		//: Keep the first signature whose last byte would be trimmed away.
		if candidate[len(candidate)-1] == '\n' {
			newlineTailed = candidate
			break
		}
	}
	//: 1-in-256 per attempt; 4096 attempts miss with probability ~1e-7. A
	//: miss is not a failure of the code under test, so the row is dropped
	//: rather than reported as one.
	if newlineTailed == nil {
		t.Log("no signature ending in 0x0a found; the regression row cannot be built")
	}

	tests := []struct {
		name string
		raw  []byte
		why  string
	}{
		{
			name: "an ordinary raw signature is accepted verbatim",
			raw:  ed25519.Sign(priv, []byte("control")),
			why:  "the control: this row passes under either ordering",
		},
		{
			name: "a raw signature whose last byte is 0x0a is accepted verbatim",
			raw:  newlineTailed,
			why:  "trimming first would drop that byte, leave 63, and fall through to the base64 branch",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: The regression row is absent only on an astronomically
			//: unlikely search miss; nothing to assert then.
			if tc.raw == nil {
				return
			}

			got, err := decodeSignature(tc.raw)
			//: Trimming before the length check refuses this input.
			if err != nil {
				t.Fatalf("decodeSignature() error = %v, want the raw signature accepted verbatim (%s)", err, tc.why)
			}
			if !slices.Equal(got, tc.raw) {
				t.Errorf("decodeSignature() = %x, want %x — the input was altered (%s)", got, tc.raw, tc.why)
			}
		})
	}
}

// TestService_canAuthenticate pins the anchor check itself, and specifically why it
// is a LENGTH test rather than a nil test.
//
// ed25519.Verify panics on a key of the wrong size, so this predicate is what
// keeps the whole verification path total: every other length — a truncated
// stamp, a key from another algorithm, a nil field on a build that was never
// stamped — has to be refused here, before any of it reaches Verify.
//
// The message must name the tag. An operator who ran `ktn-linter upgrade`
// against three candidates needs to know which install was refused, and this
// error is the only place that appears.
func TestService_canAuthenticate(t *testing.T) {
	t.Parallel()

	pub, _ := vendorKeypair(t)

	tests := []struct {
		name    string
		key     []byte
		wantErr bool
		why     string
	}{
		{
			name:    "an unstamped build cannot authenticate anything",
			key:     nil,
			wantErr: true,
			why:     "the zero value of the field is the state every unstamped build is in",
		},
		{
			name:    "an empty key is not an anchor",
			key:     []byte{},
			wantErr: true,
			why:     "same state, reached by an explicit empty stamp",
		},
		{
			name:    "a truncated key would panic ed25519.Verify",
			key:     pub[:ed25519.PublicKeySize-1],
			wantErr: true,
			why:     "this is the case that makes the check a length test and not a nil test",
		},
		{
			name:    "an over-long key is equally unusable",
			key:     append(slices.Clone(pub), 0x00),
			wantErr: true,
			why:     "Verify panics on any wrong length, not only on short ones",
		},
		{
			name:    "an exact ed25519 public key is an anchor",
			key:     pub,
			wantErr: false,
			why:     "the only length this build can verify a release with",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			svc := &Service{vendorKey: ed25519.PublicKey(tc.key), src: testSource}

			err := svc.canAuthenticate("v9.9.9-rc.1")
			//: A false negative here hands a bad key to ed25519.Verify, which
			//: panics rather than returning false.
			if (err != nil) != tc.wantErr {
				t.Fatalf("canAuthenticate() error = %v, wantErr %v (%s)", err, tc.wantErr, tc.why)
			}
			//: Nothing more to inspect on the accepting row.
			if !tc.wantErr {
				return
			}
			//: The refusal must be classifiable by the caller...
			if !errors.Is(err, coreupd.NoVendorKey) {
				t.Errorf("canAuthenticate() error = %v, want errors.Is coreupd.NoVendorKey", err)
			}
			//: ...and must say WHICH install was refused.
			if !strings.Contains(err.Error(), "v9.9.9-rc.1") {
				t.Errorf("canAuthenticate() error = %q, want it to name the tag", err.Error())
			}
		})
	}
}
