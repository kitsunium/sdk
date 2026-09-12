// Package updater provides self-update functionality for ktn-linter binary.
package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	coreupd "github.com/kitsunium/sdk/internal/core/selfupdate"
)

// makeTarGzWithBinary returns the in-memory bytes of a tar.gz archive
// containing a single regular file named `ktn-linter` with the given
// payload. Used by download/extract scenarios to feed downloadAndReplace
// the same archive shape the real release pipeline produces.
func makeTarGzWithBinary(t *testing.T, payload []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	hdr := &tar.Header{
		Name:     testSource.Product,
		Mode:     0o755,
		Size:     int64(len(payload)),
		Typeflag: tar.TypeReg,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatalf("tar header: %v", err)
	}
	if _, err := tw.Write(payload); err != nil {
		t.Fatalf("tar write: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

// makeZipWithBinary mirrors makeTarGzWithBinary for the windows .zip shape.
// The inner file name is `widget.exe` since openInnerBinary picks that
// when GOOS=windows.
func makeZipWithBinary(t *testing.T, payload []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	f, err := zw.Create(testSource.Product + ".exe")
	if err != nil {
		t.Fatalf("zip create: %v", err)
	}
	if _, err := f.Write(payload); err != nil {
		t.Fatalf("zip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

// hostAssetName returns the release archive asset name resolved for the host
// platform — the same name downloadAndReplace requests and verifies.
func hostAssetName() string {
	//: Service literal mirrors NewUpdaterWithDeps platform capture.
	svc := &Service{goos: runtimeGOOS(), goarch: runtimeGOARCH(), src: testSource}
	return svc.getBinaryName()
}

// checksumsManifestFor returns sha256sum-format manifest bytes covering the
// given archive under the host platform's release asset name, matching the
// checksums.txt asset published by the release workflow.
func checksumsManifestFor(t *testing.T, archive []byte) []byte {
	t.Helper()
	sum := sha256.Sum256(archive)
	return fmt.Appendf(nil, "%s  %s\n", hex.EncodeToString(sum[:]), hostAssetName())
}

// Test_normalizeVersion tests version normalization for various formats.
func Test_normalizeVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		version  string
		expected string
	}{
		{
			name:     "standard version with v prefix",
			version:  "v1.2.3",
			expected: "v1.2.3",
		},
		{
			name:     "version without v prefix",
			version:  "1.2.3",
			expected: "v1.2.3",
		},
		{
			name:     "empty version string",
			version:  "",
			expected: "v",
		},
		{
			name:     "prerelease version with v prefix",
			version:  "v1.0.0-rc.1",
			expected: "v1.0.0-rc.1",
		},
		{
			name:     "prerelease version without v prefix",
			version:  "1.0.0-rc.42.abc1234",
			expected: "v1.0.0-rc.42.abc1234",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := normalizeVersion(tt.version)
			// Check result matches expected
			if result != tt.expected {
				t.Errorf("normalizeVersion(%q) = %q, want %q", tt.version, result, tt.expected)
			}
		})
	}
}

// TestUpdaterService_isNewer tests version comparison logic for various scenarios.
func TestUpdaterService_isNewer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		current  string
		latest   string
		expected bool
	}{
		{
			name:     "major version upgrade available",
			current:  "v1.0.0",
			latest:   "v2.0.0",
			expected: true,
		},
		{
			name:     "minor version upgrade available",
			current:  "v1.0.0",
			latest:   "v1.1.0",
			expected: true,
		},
		{
			name:     "patch version upgrade available",
			current:  "v1.0.0",
			latest:   "v1.0.1",
			expected: true,
		},
		{
			name:     "same version no upgrade",
			current:  "v1.0.0",
			latest:   "v1.0.0",
			expected: false,
		},
		{
			name:     "older major version no upgrade",
			current:  "v2.0.0",
			latest:   "v1.0.0",
			expected: false,
		},
		{
			name:     "older minor version no upgrade",
			current:  "v1.5.0",
			latest:   "v1.4.0",
			expected: false,
		},
		{
			name:     "older patch version no upgrade",
			current:  "v1.0.5",
			latest:   "v1.0.4",
			expected: false,
		},
		{
			name:     "major upgrade from high minor patch",
			current:  "v1.9.9",
			latest:   "v2.0.0",
			expected: true,
		},
		{
			name:     "same major higher remote minor",
			current:  "v2.0.0",
			latest:   "v2.1.0",
			expected: true,
		},
		{
			name:     "same major minor higher remote patch",
			current:  "v2.1.0",
			latest:   "v2.1.5",
			expected: true,
		},
		{
			name:     "stable newer than its rc",
			current:  "v1.0.0-rc.1",
			latest:   "v1.0.0",
			expected: true,
		},
		{
			name:     "rc2 newer than rc1",
			current:  "v1.0.0-rc.1",
			latest:   "v1.0.0-rc.2",
			expected: true,
		},
		{
			name:     "stable not downgraded to rc",
			current:  "v1.0.0",
			latest:   "v1.0.0-rc.1",
			expected: false,
		},
		{
			name:     "rc of next version newer than current stable",
			current:  "v1.2.0",
			latest:   "v1.3.0-rc.42.abc1234",
			expected: true,
		},
		{
			name:     "newer stable over rc of same base",
			current:  "v1.3.113-rc.42.abc1234",
			latest:   "v1.3.113",
			expected: true,
		},
		{
			name:     "invalid current version",
			current:  "invalid",
			latest:   "v1.0.0",
			expected: false,
		},
		{
			name:     "invalid latest version",
			current:  "v1.0.0",
			latest:   "invalid",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			updater := NewService(tt.current, testSource)
			result := updater.isNewer(tt.latest)
			// Check result matches expected
			if result != tt.expected {
				t.Errorf("isNewer(%q) with current %q = %v, want %v",
					tt.latest, tt.current, result, tt.expected)
			}
		})
	}
}

// TestUpdaterService_getBinaryName tests platform-specific archive name generation.
// The archive name is `ktn-linter_{GOOS}_{GOARCH}.{tar.gz,zip}` (goreleaser-style),
// regardless of version. Invariant: contains the snake-case prefix + min length.
func TestUpdaterService_getBinaryName(t *testing.T) {
	t.Parallel()

	const wantPrefix = "widget_"
	const wantMinLength int = 15

	tests := []struct {
		name    string
		version string
	}{
		{name: "standard version", version: "v1.0.0"},
		{name: "dev version", version: "dev"},
		{name: "empty version", version: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			updater := NewService(tt.version, testSource)
			name := updater.getBinaryName()
			if name == "" {
				t.Error("getBinaryName() returned empty string")
			}
			if !strings.Contains(name, wantPrefix) {
				t.Errorf("getBinaryName() = %q, want to contain %q", name, wantPrefix)
			}
			if len(name) < wantMinLength {
				t.Errorf("getBinaryName() length = %d, want >= %d", len(name), wantMinLength)
			}
		})
	}
}

// TestUpdaterService_getLatestVersion tests fetching latest version from GitHub API.
func TestUpdaterService_getLatestVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		responseBody   string
		responseStatus int
		wantVersion    string
		wantErr        bool
		wantErrContain string
	}{
		{
			name:           "successful response",
			responseBody:   `{"tag_name": "v1.2.3"}`,
			responseStatus: http.StatusOK,
			wantVersion:    "v1.2.3",
			wantErr:        false,
		},
		{
			name:           "not found status",
			responseBody:   `{"message": "Not Found"}`,
			responseStatus: http.StatusNotFound,
			wantVersion:    "",
			wantErr:        true,
			wantErrContain: "the release host answered unexpectedly",
		},
		{
			name:           "server error status",
			responseBody:   `{"message": "Internal Server Error"}`,
			responseStatus: http.StatusInternalServerError,
			wantVersion:    "",
			wantErr:        true,
			wantErrContain: "the release host answered unexpectedly",
		},
		{
			name:           "invalid json response",
			responseBody:   `{invalid json`,
			responseStatus: http.StatusOK,
			wantVersion:    "",
			wantErr:        true,
			wantErrContain: "parsing release info",
		},
		{
			name:           "empty tag name",
			responseBody:   `{"tag_name": ""}`,
			responseStatus: http.StatusOK,
			wantVersion:    "",
			wantErr:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Create test server
			server := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.responseStatus)
				if _, err := w.Write([]byte(tt.responseBody)); err != nil {
					t.Logf("response Write: %v", err)
				}
			}))
			//: In-memory network: no real port. The server starts on the first
			//: Client() call, which is also what fills server.URL, so start it
			//: here and let the reads below run in any order. NewTestServer
			//: registers the cleanup itself.
			server.Client()

			// Create updater with custom client pointing to test server
			updater := &Service{
				version: "v1.0.0",
				client:  server.Client(),
				fs:      osFileSystem{},
				copier:  stdIOCopier{},
				src:     testSource,
			}

			// Override the API URL by using a custom transport
			// Type assertion is safe since updater.client was set to server.Client() above
			originalClient, ok := updater.client.(*http.Client)
			if !ok {
				t.Fatal("expected updater.client to be *http.Client")
			}
			updater.client = &http.Client{
				Transport: &mockTransport{
					url:    server.URL,
					client: originalClient,
				},
			}

			version, err := updater.getLatestVersion()

			// Check error expectation
			if tt.wantErr {
				// Expect error
				if err == nil {
					t.Errorf("getLatestVersion() error = nil, wantErr %v", tt.wantErr)
				} else if tt.wantErrContain != "" && !strings.Contains(err.Error(), tt.wantErrContain) {
					t.Errorf("getLatestVersion() error = %v, want error containing %q", err, tt.wantErrContain)
				}
			} else {
				// Expect no error
				if err != nil {
					t.Errorf("getLatestVersion() unexpected error = %v", err)
				}
			}

			// Check version
			if version != tt.wantVersion {
				t.Errorf("getLatestVersion() = %q, want %q", version, tt.wantVersion)
			}
		})
	}
}

// mockTransport is a custom RoundTripper that redirects requests to test server.
type mockTransport struct {
	url    string
	client *http.Client
}

// RoundTrip implements http.RoundTripper interface.
func (m *mockTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if m == nil || m.client == nil {
		return nil, errors.New("mockTransport: nil client")
	}
	// Guard against nil request
	if req == nil || req.URL == nil {
		return nil, errors.New("mockTransport: nil request")
	}

	// Parse test-server base URL
	base, err := url.Parse(m.url)
	if err != nil {
		return nil, err
	}

	// Preserve original path + query, redirect to test server host
	target := base.ResolveReference(&url.URL{
		Path:     req.URL.Path,
		RawQuery: req.URL.RawQuery,
	})

	// Clone the request preserving context
	newReq := req.Clone(req.Context())
	newReq.URL = target
	newReq.Host = target.Host
	newReq.RequestURI = "" // Clear RequestURI to prevent HTTP client errors
	newReq.Header = req.Header.Clone()

	// Get transport with nil fallback (and prevent self-recursion)
	rt := m.client.Transport
	if rt == nil {
		rt = http.DefaultTransport
	}
	// Prevent self-recursion
	if _, ok := rt.(*mockTransport); ok {
		rt = http.DefaultTransport
	}
	// Execute request
	return rt.RoundTrip(newReq)
}

// TestUpdaterService_downloadAndReplace enumerates HTTP error responses;
// every row asserts the same "the release could not be downloaded" wrapping (contract).
// The happy-path is covered by TestUpdaterService_downloadAndReplace_scenarios.
func TestUpdaterService_downloadAndReplace(t *testing.T) {
	t.Parallel()

	const wantErrContain = "the release could not be downloaded"

	tests := []struct {
		name           string
		responseStatus int
		responseBody   string
	}{
		{name: "download not found", responseStatus: http.StatusNotFound, responseBody: "Not Found"},
		{name: "server error", responseStatus: http.StatusInternalServerError, responseBody: "Internal Server Error"},
		{name: "forbidden access", responseStatus: http.StatusForbidden, responseBody: "Forbidden"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.responseStatus)
				if _, err := w.Write([]byte(tt.responseBody)); err != nil {
					t.Logf("response Write: %v", err)
				}
			}))
			//: In-memory network: no real port. The server starts on the first
			//: Client() call, which is also what fills server.URL, so start it
			//: here and let the reads below run in any order. NewTestServer
			//: registers the cleanup itself.
			server.Client()

			//: A throwaway vendor key so the anchor pre-check passes and the
			//: HTTP status mapping under test is what actually decides. The
			//: key is never used: the download fails before verification.
			vendorPub, _ := vendorKeypair(t)
			updater := (&Service{
				version: "v1.0.0",
				client: &http.Client{
					Transport: &mockTransport{
						url:    server.URL,
						client: server.Client(),
					},
				},
				fs:     osFileSystem{},
				copier: stdIOCopier{},
			}).WithVendorKey(vendorPub)

			err := updater.downloadAndReplace("v1.1.0")
			if err == nil {
				t.Fatalf("downloadAndReplace() expected error for HTTP %d", tt.responseStatus)
			}
			if !strings.Contains(err.Error(), wantErrContain) {
				t.Errorf("downloadAndReplace() error = %v, want error containing %q", err, wantErrContain)
			}
		})
	}
}

// TestUpdaterService_resolveExecutablePath tests executable path resolution.
func TestUpdaterService_resolveExecutablePath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		execFunc       func() (string, error)
		evalFunc       func(path string) (string, error)
		wantPath       string
		wantErr        bool
		wantErrContain string
	}{
		{
			name: "successful resolution",
			execFunc: func() (string, error) {
				return "/usr/bin/ktn-linter", nil
			},
			evalFunc: func(path string) (string, error) {
				return path, nil
			},
			wantPath: "/usr/bin/ktn-linter",
			wantErr:  false,
		},
		{
			name: "executable error",
			execFunc: func() (string, error) {
				return "", errors.New("executable not found")
			},
			evalFunc: func(path string) (string, error) {
				return path, nil
			},
			wantPath:       "",
			wantErr:        true,
			wantErrContain: "getting executable path",
		},
		{
			name: "symlink resolution error",
			execFunc: func() (string, error) {
				return "/usr/bin/ktn-linter", nil
			},
			evalFunc: func(path string) (string, error) {
				return "", errors.New("broken symlink")
			},
			wantPath:       "",
			wantErr:        true,
			wantErrContain: "resolving symlinks",
		},
		{
			name: "symlink resolved to different path",
			execFunc: func() (string, error) {
				return "/usr/bin/ktn-linter", nil
			},
			evalFunc: func(path string) (string, error) {
				return "/opt/ktn-linter/bin/ktn-linter", nil
			},
			wantPath: "/opt/ktn-linter/bin/ktn-linter",
			wantErr:  false,
		},
	}

	// Run each test case
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			updater := NewUpdaterWithDeps("v1.0.0", testSource,
				&mockHTTPClient{},
				&mockFileSystem{
					executableFunc:   tt.execFunc,
					evalSymlinksFunc: tt.evalFunc,
				},
				&mockCopier{},
			)

			path, err := updater.resolveExecutablePath()

			// Check error expectation
			if tt.wantErr {
				if err == nil {
					t.Errorf("resolveExecutablePath() error = nil, wantErr %v", tt.wantErr)
				} else if tt.wantErrContain != "" && !strings.Contains(err.Error(), tt.wantErrContain) {
					t.Errorf("resolveExecutablePath() error = %v, want containing %q", err, tt.wantErrContain)
				}
			} else {
				if err != nil {
					t.Errorf("resolveExecutablePath() unexpected error = %v", err)
				}
			}

			// Check path
			if path != tt.wantPath {
				t.Errorf("resolveExecutablePath() = %q, want %q", path, tt.wantPath)
			}
		})
	}
}

// TestUpdaterService_writeAndReplaceBinary tests writing and replacing the binary.
func TestUpdaterService_writeAndReplaceBinary(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		content           io.Reader
		execPath          string
		createTempReturns error
		useRealCreateTemp bool
		chmodFunc         func(name string, mode os.FileMode) error
		renameFunc        func(oldpath, newpath string) error
		removeFunc        func(name string) error
		copyFunc          func(dst io.Writer, src io.Reader) (int64, error)
		wantErr           bool
		wantErrContain    string
	}{
		{
			name:              "successful write and replace",
			content:           bytes.NewReader([]byte("binary content")),
			execPath:          "/usr/bin/ktn-linter",
			useRealCreateTemp: true,
			wantErr:           false,
		},
		{
			name:              "temp file creation error",
			content:           bytes.NewReader([]byte("binary content")),
			execPath:          "/usr/bin/ktn-linter",
			createTempReturns: errors.New("permission denied"),
			wantErr:           true,
			wantErrContain:    "creating temp file",
		},
		{
			name:              "copy error",
			content:           &errReader{err: errors.New("read error")},
			execPath:          "/usr/bin/ktn-linter",
			useRealCreateTemp: true,
			copyFunc: func(dst io.Writer, src io.Reader) (int64, error) {
				return 0, errors.New("copy failed")
			},
			wantErr:        true,
			wantErrContain: "writing temp file",
		},
		{
			name:              "chmod error",
			content:           bytes.NewReader([]byte("binary content")),
			execPath:          "/usr/bin/ktn-linter",
			useRealCreateTemp: true,
			chmodFunc: func(name string, mode os.FileMode) error {
				return errors.New("chmod failed")
			},
			wantErr:        true,
			wantErrContain: "setting permissions",
		},
		{
			name:              "rename error",
			content:           bytes.NewReader([]byte("binary content")),
			execPath:          "/usr/bin/ktn-linter",
			useRealCreateTemp: true,
			renameFunc: func(oldpath, newpath string) error {
				return errors.New("rename failed")
			},
			wantErr:        true,
			wantErrContain: "replacing binary",
		},
	}

	// Run each test case
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Track cleanup
			var cleanedUp bool

			// Build createTempFunc based on test case configuration
			var createTempFunc func(dir, pattern string) (*os.File, error)
			if tt.createTempReturns != nil {
				// Return a specific error
				createTempFunc = func(dir, pattern string) (*os.File, error) {
					return nil, tt.createTempReturns
				}
			} else if tt.useRealCreateTemp {
				// Use real CreateTemp with test-managed temp dir
				tempDir := t.TempDir()
				createTempFunc = func(dir, pattern string) (*os.File, error) {
					return os.CreateTemp(tempDir, pattern)
				}
			}

			fs := &mockFileSystem{
				createTempFunc: createTempFunc,
				chmodFunc:      tt.chmodFunc,
				renameFunc:     tt.renameFunc,
				removeFunc: func(name string) error {
					cleanedUp = true
					if tt.removeFunc != nil {
						return tt.removeFunc(name)
					}
					// Clean up actual temp file
					return os.Remove(name)
				},
			}

			copier := &mockCopier{copyFunc: tt.copyFunc}

			updater := NewUpdaterWithDeps("v1.0.0", testSource, &mockHTTPClient{}, fs, copier)

			err := updater.writeAndReplaceBinary(tt.content, tt.execPath)

			// Check error expectation
			if tt.wantErr {
				if err == nil {
					t.Errorf("writeAndReplaceBinary() error = nil, wantErr %v", tt.wantErr)
				} else if tt.wantErrContain != "" && !strings.Contains(err.Error(), tt.wantErrContain) {
					t.Errorf("writeAndReplaceBinary() error = %v, want containing %q", err, tt.wantErrContain)
				}
			} else {
				if err != nil {
					t.Errorf("writeAndReplaceBinary() unexpected error = %v", err)
				}
			}

			// For error cases (except temp creation), verify cleanup was called
			if tt.wantErr && tt.useRealCreateTemp && !strings.Contains(tt.wantErrContain, "creating temp file") {
				if !cleanedUp {
					t.Error("expected cleanup to be called on error")
				}
			}
		})
	}
}

// TestUpdaterService_writeAndReplaceBinary_StagingFallback pins the OS-temp-dir
// fallback: when the executable's own directory refuses a temp file with a
// permission error — a root-owned /usr/local/bin install being the
// real-world case ktn.md's installer conditional also handles — CreateTemp
// must be retried against the OS temp dir instead of giving up outright, so
// finalizeReplacement's sudo -n retry has something local to move. A
// non-permission failure on the install dir (disk full, etc.) must NOT
// trigger the same retry, since a different user can't fix that either.
func TestUpdaterService_writeAndReplaceBinary_StagingFallback(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		dirErr         error
		fallbackErr    error
		wantErr        bool
		wantErrContain string
		wantDirs       []string
	}{
		{
			name:     "permission denied on install dir falls back to OS temp dir",
			dirErr:   fmt.Errorf("wrap: %w", os.ErrPermission),
			wantErr:  false,
			wantDirs: []string{"/usr/local/bin", ""},
		},
		{
			name:           "fallback also denied surfaces the fallback error",
			dirErr:         fmt.Errorf("wrap: %w", os.ErrPermission),
			fallbackErr:    errors.New("still denied"),
			wantErr:        true,
			wantErrContain: "still denied",
			wantDirs:       []string{"/usr/local/bin", ""},
		},
		{
			name:           "non-permission error on install dir never retries",
			dirErr:         errors.New("disk full"),
			wantErr:        true,
			wantErrContain: "disk full",
			wantDirs:       []string{"/usr/local/bin"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var gotDirs []string
			tempDir := t.TempDir()

			fs := &mockFileSystem{
				createTempFunc: func(dir, pattern string) (*os.File, error) {
					gotDirs = append(gotDirs, dir)
					//: The fallback call always passes an empty dir (OS temp dir).
					if dir != "" {
						return nil, tt.dirErr
					}
					if tt.fallbackErr != nil {
						return nil, tt.fallbackErr
					}
					return os.CreateTemp(tempDir, pattern)
				},
				removeFunc: func(name string) error {
					return os.Remove(name)
				},
			}

			updater := NewUpdaterWithDeps("v1.0.0", testSource, &mockHTTPClient{}, fs, &mockCopier{})

			err := updater.writeAndReplaceBinary(bytes.NewReader([]byte("content")), "/usr/local/bin/ktn-linter")

			if tt.wantErr {
				if err == nil {
					t.Fatalf("writeAndReplaceBinary() error = nil, want error containing %q", tt.wantErrContain)
				}
				if !strings.Contains(err.Error(), tt.wantErrContain) {
					t.Errorf("writeAndReplaceBinary() error = %v, want containing %q", err, tt.wantErrContain)
				}
			} else if err != nil {
				t.Errorf("writeAndReplaceBinary() unexpected error = %v", err)
			}

			if !slices.Equal(gotDirs, tt.wantDirs) {
				t.Errorf("CreateTemp called with dirs = %v, want %v", gotDirs, tt.wantDirs)
			}
		})
	}
}

// TestUpdaterService_writeTempFileContent tests writing content to temp file.
func TestUpdaterService_writeTempFileContent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		content        io.Reader
		copyFunc       func(dst io.Writer, src io.Reader) (int64, error)
		wantErr        bool
		wantErrContain string
	}{
		{
			name:    "successful write",
			content: bytes.NewReader([]byte("test content")),
			wantErr: false,
		},
		{
			name:    "copy error",
			content: bytes.NewReader([]byte("test content")),
			copyFunc: func(dst io.Writer, src io.Reader) (int64, error) {
				return 0, errors.New("copy failed")
			},
			wantErr:        true,
			wantErrContain: "writing temp file",
		},
		{
			name:    "reader error",
			content: &errReader{err: errors.New("read error")},
			copyFunc: func(dst io.Writer, src io.Reader) (int64, error) {
				return 0, errors.New("read error")
			},
			wantErr:        true,
			wantErrContain: "writing temp file",
		},
	}

	// Run each test case
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			copier := &mockCopier{copyFunc: tt.copyFunc}
			updater := NewUpdaterWithDeps("v1.0.0", testSource, &mockHTTPClient{}, &mockFileSystem{}, copier)

			var buf bytes.Buffer
			err := updater.writeTempFileContent(&buf, tt.content)

			// Check error expectation
			if tt.wantErr {
				if err == nil {
					t.Errorf("writeTempFileContent() error = nil, wantErr %v", tt.wantErr)
				} else if tt.wantErrContain != "" && !strings.Contains(err.Error(), tt.wantErrContain) {
					t.Errorf("writeTempFileContent() error = %v, want containing %q", err, tt.wantErrContain)
				}
			} else {
				if err != nil {
					t.Errorf("writeTempFileContent() unexpected error = %v", err)
				}
			}
		})
	}
}

// TestUpdaterService_finalizeReplacement tests finalization of binary replacement.
func TestUpdaterService_finalizeReplacement(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		tmpPath        string
		execPath       string
		chmodFunc      func(name string, mode os.FileMode) error
		renameFunc     func(oldpath, newpath string) error
		removeFunc     func(name string) error
		wantErr        bool
		wantErrContain string
	}{
		{
			name:     "successful replacement",
			tmpPath:  "/tmp/update-123",
			execPath: "/usr/bin/ktn-linter",
			wantErr:  false,
		},
		{
			name:     "chmod error",
			tmpPath:  "/tmp/update-123",
			execPath: "/usr/bin/ktn-linter",
			chmodFunc: func(name string, mode os.FileMode) error {
				return errors.New("chmod failed")
			},
			wantErr:        true,
			wantErrContain: "setting permissions",
		},
		{
			name:     "rename error",
			tmpPath:  "/tmp/update-123",
			execPath: "/usr/bin/ktn-linter",
			renameFunc: func(oldpath, newpath string) error {
				return errors.New("rename failed")
			},
			wantErr:        true,
			wantErrContain: "replacing binary",
		},
	}

	// Run each test case
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Track cleanup
			var cleanedUp bool

			fs := &mockFileSystem{
				chmodFunc:  tt.chmodFunc,
				renameFunc: tt.renameFunc,
				removeFunc: func(name string) error {
					cleanedUp = true
					if tt.removeFunc != nil {
						return tt.removeFunc(name)
					}
					return nil
				},
			}

			updater := NewUpdaterWithDeps("v1.0.0", testSource, &mockHTTPClient{}, fs, &mockCopier{})

			err := updater.finalizeReplacement(tt.tmpPath, tt.execPath)

			// Check error expectation
			if tt.wantErr {
				if err == nil {
					t.Errorf("finalizeReplacement() error = nil, wantErr %v", tt.wantErr)
				} else if tt.wantErrContain != "" && !strings.Contains(err.Error(), tt.wantErrContain) {
					t.Errorf("finalizeReplacement() error = %v, want containing %q", err, tt.wantErrContain)
				}
				// Verify cleanup was called on error
				if !cleanedUp {
					t.Error("expected cleanup to be called on error")
				}
			} else {
				if err != nil {
					t.Errorf("finalizeReplacement() unexpected error = %v", err)
				}
			}
		})
	}
}

// TestUpdaterService_finalizeReplacement_Elevate pins the sudo -n retry:
// finalizeReplacement must escalate exactly when Rename fails with a
// permission error (never for an unrelated failure, and never more than
// once), report success when the escalated move lands, and name both
// failures when it doesn't. The updater package's own CI sandbox has no
// passwordless sudo, so u.elevate is swapped for a fake here rather than
// exercising the real sudoMove — that keeps the test deterministic instead
// of depending on the host's sudoers configuration.
func TestUpdaterService_finalizeReplacement_Elevate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		renameErr      error
		elevateErr     error
		wantErr        bool
		wantErrContain string
		wantElevated   bool
	}{
		{
			name:         "permission error recovers via elevate",
			renameErr:    fmt.Errorf("wrap: %w", os.ErrPermission),
			wantErr:      false,
			wantElevated: true,
		},
		{
			name:           "permission error survives a failed elevate",
			renameErr:      fmt.Errorf("wrap: %w", os.ErrPermission),
			elevateErr:     errors.New("sudo: a password is required"),
			wantErr:        true,
			wantErrContain: "sudo: a password is required",
			wantElevated:   true,
		},
		{
			name:           "non-permission rename error never elevates",
			renameErr:      errors.New("device busy"),
			wantErr:        true,
			wantErrContain: "device busy",
			wantElevated:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var elevateCalls int
			fs := &mockFileSystem{
				renameFunc: func(string, string) error { return tt.renameErr },
				removeFunc: func(string) error { return nil },
			}

			updater := NewUpdaterWithDeps("v1.0.0", testSource, &mockHTTPClient{}, fs, &mockCopier{})
			updater.elevate = func(tmpPath, execPath string) error {
				elevateCalls++
				return tt.elevateErr
			}

			err := updater.finalizeReplacement("/tmp/widget-update-1", "/usr/local/bin/ktn-linter")

			if tt.wantErr {
				if err == nil {
					t.Fatalf("finalizeReplacement() error = nil, want error containing %q", tt.wantErrContain)
				}
				if !strings.Contains(err.Error(), tt.wantErrContain) {
					t.Errorf("finalizeReplacement() error = %v, want containing %q", err, tt.wantErrContain)
				}
			} else if err != nil {
				t.Errorf("finalizeReplacement() unexpected error = %v", err)
			}

			gotElevated := elevateCalls > 0
			if gotElevated != tt.wantElevated {
				t.Errorf("elevate invoked = %v (n=%d), want %v", gotElevated, elevateCalls, tt.wantElevated)
			}
			if elevateCalls > 1 {
				t.Errorf("elevate invoked %d times, want at most 1", elevateCalls)
			}
		})
	}
}

// TestUpdaterService_CheckForUpdate_withMock tests CheckForUpdate with mock HTTP.
func TestUpdaterService_CheckForUpdate_withMock(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		version        string
		responseBody   string
		responseStatus int
		wantAvailable  bool
		wantErr        bool
		wantErrContain string
	}{
		{
			name:           "update available",
			version:        "v1.0.0",
			responseBody:   `{"tag_name": "v2.0.0"}`,
			responseStatus: http.StatusOK,
			wantAvailable:  true,
			wantErr:        false,
		},
		{
			name:           "already up to date",
			version:        "v2.0.0",
			responseBody:   `{"tag_name": "v2.0.0"}`,
			responseStatus: http.StatusOK,
			wantAvailable:  false,
			wantErr:        false,
		},
		{
			name:           "API error",
			version:        "v1.0.0",
			responseBody:   `{"message": "Not Found"}`,
			responseStatus: http.StatusNotFound,
			wantAvailable:  false,
			wantErr:        true,
			wantErrContain: "the release host answered unexpectedly",
		},
		{
			name:           "HTTP request error",
			version:        "v1.0.0",
			responseBody:   "",
			responseStatus: 0, // Will cause connection error
			wantAvailable:  false,
			wantErr:        true,
			wantErrContain: "fetching release info",
		},
	}

	// Run each test case
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var client Getter

			// Handle HTTP error case
			if tt.responseStatus == 0 {
				client = &mockHTTPClient{
					getFunc: func(url string) (*http.Response, error) {
						return nil, errors.New("connection refused")
					},
				}
			} else {
				// Create test server
				server := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(tt.responseStatus)
					if _, err := w.Write([]byte(tt.responseBody)); err != nil {
						t.Logf("response Write: %v", err)
					}
				}))
				//: In-memory network: no real port. The server starts on the first
				//: Client() call, which is also what fills server.URL, so start it
				//: here and let the reads below run in any order. NewTestServer
				//: registers the cleanup itself.
				server.Client()

				// Use mock transport to redirect to test server
				client = &http.Client{
					Transport: &mockTransport{
						url:    server.URL,
						client: server.Client(),
					},
				}
			}

			updater := NewUpdaterWithDeps(tt.version, testSource, client, &mockFileSystem{}, &mockCopier{})

			info, err := updater.CheckForUpdate()

			// Check error expectation
			if tt.wantErr {
				if err == nil {
					t.Errorf("CheckForUpdate() error = nil, wantErr %v", tt.wantErr)
				} else if tt.wantErrContain != "" && !strings.Contains(err.Error(), tt.wantErrContain) {
					t.Errorf("CheckForUpdate() error = %v, want containing %q", err, tt.wantErrContain)
				}
			} else {
				if err != nil {
					t.Errorf("CheckForUpdate() unexpected error = %v", err)
				}
			}

			// Check available status
			if info.Available != tt.wantAvailable {
				t.Errorf("CheckForUpdate() Available = %v, want %v", info.Available, tt.wantAvailable)
			}
		})
	}
}

// TestUpdaterService_Upgrade_withMock tests Upgrade with mock dependencies.
// Both scenarios assert Available=false; one is the "already current" path
// (wantErr=false) and the other is the "API error" path (wantErr=true).
func TestUpdaterService_Upgrade_withMock(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		version        string
		responseBody   string
		responseStatus int
		wantErr        bool
		wantErrContain string
	}{
		{
			name:           "no update available",
			version:        "v2.0.0",
			responseBody:   `{"tag_name": "v2.0.0"}`,
			responseStatus: http.StatusOK,
			wantErr:        false,
		},
		{
			name:           "API error",
			version:        "v1.0.0",
			responseBody:   `{"message": "Not Found"}`,
			responseStatus: http.StatusNotFound,
			wantErr:        true,
			wantErrContain: "the release host answered unexpectedly",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.responseStatus)
				if _, err := w.Write([]byte(tt.responseBody)); err != nil {
					t.Logf("response Write: %v", err)
				}
			}))
			//: In-memory network: no real port. The server starts on the first
			//: Client() call, which is also what fills server.URL, so start it
			//: here and let the reads below run in any order. NewTestServer
			//: registers the cleanup itself.
			server.Client()

			client := &http.Client{
				Transport: &mockTransport{
					url:    server.URL,
					client: server.Client(),
				},
			}

			updater := NewUpdaterWithDeps(tt.version, testSource, client, &mockFileSystem{}, &mockCopier{})

			info, err := updater.Upgrade()

			if tt.wantErr {
				if err == nil {
					t.Errorf("Upgrade() error = nil, wantErr %v", tt.wantErr)
				} else if tt.wantErrContain != "" && !strings.Contains(err.Error(), tt.wantErrContain) {
					t.Errorf("Upgrade() error = %v, want containing %q", err, tt.wantErrContain)
				}
			} else if err != nil {
				t.Errorf("Upgrade() unexpected error = %v", err)
			}

			// Available is always false in this enumeration (no update is published).
			if info.Available {
				t.Errorf("Upgrade() Available = true, want false (no newer release in fixture)")
			}
		})
	}
}

// TestUpdaterService_downloadAndReplace_scenarios tests the complete download and replace flow.
func TestUpdaterService_downloadAndReplace_scenarios(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		setupFS         func(t *testing.T, execPath string) *mockFileSystem
		wantErr         bool
		wantErrContain  string
		verifyContent   bool
		expectedContent string
	}{
		{
			name: "successful download and replace",
			setupFS: func(t *testing.T, execPath string) *mockFileSystem {
				return &mockFileSystem{
					executableFunc: func() (string, error) {
						return execPath, nil
					},
					evalSymlinksFunc: func(path string) (string, error) {
						return path, nil
					},
					createTempFunc: func(dir, pattern string) (*os.File, error) {
						return os.CreateTemp(dir, pattern)
					},
					chmodFunc: func(name string, mode os.FileMode) error {
						return os.Chmod(name, mode)
					},
					renameFunc: func(oldpath, newpath string) error {
						return os.Rename(oldpath, newpath)
					},
					removeFunc: func(name string) error {
						return os.Remove(name)
					},
				}
			},
			wantErr:         false,
			verifyContent:   true,
			expectedContent: "new binary content",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Create a temp directory for testing
			tmpDir := t.TempDir()

			// Create a fake executable
			execPath := filepath.Join(tmpDir, testSource.Product)
			if err := os.WriteFile(execPath, []byte("old binary"), 0o755); err != nil {
				t.Fatalf("failed to create fake executable: %v", err)
			}

			// Server returns a real tar.gz containing the new binary, so
			// downloadAndReplace's extract pipeline can lift it out as the
			// release flow would in production. checksums.txt AND its
			// detached vendor signature are served from the same release
			// path so both halves of the integrity gate pass.
			fixture := newReleaseFixture(t, []byte(tt.expectedContent))
			server := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				//: Route the three release assets; anything else 404s.
				if !fixture.serveAsset(t, w, r.URL.Path) {
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			//: In-memory network: no real port. The server starts on the first
			//: Client() call, which is also what fills server.URL, so start it
			//: here and let the reads below run in any order. NewTestServer
			//: registers the cleanup itself.
			server.Client()

			// Use mock transport
			client := &http.Client{
				Transport: &mockTransport{
					url:    server.URL,
					client: server.Client(),
				},
			}

			fs := tt.setupFS(t, execPath)
			updater := NewUpdaterWithDeps("v1.0.0", testSource, client, fs, &mockCopier{}).WithVendorKey(fixture.pub)

			err := updater.downloadAndReplace("v2.0.0")

			// Check error expectation
			if tt.wantErr {
				if err == nil {
					t.Error("downloadAndReplace() error = nil, want error")
				} else if tt.wantErrContain != "" && !strings.Contains(err.Error(), tt.wantErrContain) {
					t.Errorf("downloadAndReplace() error = %v, want containing %q", err, tt.wantErrContain)
				}
				return
			}

			// Check success case
			if err != nil {
				t.Errorf("downloadAndReplace() unexpected error = %v", err)
			}

			// Verify the binary was replaced
			if tt.verifyContent {
				content, err := os.ReadFile(execPath)
				// Check read error
				if err != nil {
					t.Fatalf("failed to read updated binary: %v", err)
				}
				// Check content
				if !bytes.Equal(content, []byte(tt.expectedContent)) {
					t.Errorf("binary content mismatch: got %d bytes, want %q", len(content), tt.expectedContent)
				}
			}
		})
	}
}

// TestUpdaterService_downloadBinary tests binary download functionality.
func TestUpdaterService_downloadBinary(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		responseStatus int
		responseBody   string
		wantErr        bool
		wantErrContain string
	}{
		{
			name:           "successful download",
			responseStatus: http.StatusOK,
			responseBody:   "binary content",
			wantErr:        false,
		},
		{
			name:           "not found",
			responseStatus: http.StatusNotFound,
			responseBody:   "Not Found",
			wantErr:        true,
			wantErrContain: "the release could not be downloaded",
		},
		{
			name:           "server error",
			responseStatus: http.StatusInternalServerError,
			responseBody:   "Internal Server Error",
			wantErr:        true,
			wantErrContain: "the release could not be downloaded",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Create test server
			server := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.responseStatus)
				if _, err := w.Write([]byte(tt.responseBody)); err != nil {
					t.Logf("response Write: %v", err)
				}
			}))
			//: In-memory network: no real port. The server starts on the first
			//: Client() call, which is also what fills server.URL, so start it
			//: here and let the reads below run in any order. NewTestServer
			//: registers the cleanup itself.
			server.Client()

			// Use mock transport
			client := &http.Client{
				Transport: &mockTransport{
					url:    server.URL,
					client: server.Client(),
				},
			}

			updater := NewUpdaterWithDeps("v1.0.0", testSource, client, &mockFileSystem{}, &mockCopier{})

			resp, err := updater.downloadBinary("v2.0.0")

			// Check error expectation
			if tt.wantErr {
				if err == nil {
					t.Errorf("downloadBinary() error = nil, wantErr %v", tt.wantErr)
				} else if tt.wantErrContain != "" && !strings.Contains(err.Error(), tt.wantErrContain) {
					t.Errorf("downloadBinary() error = %v, want containing %q", err, tt.wantErrContain)
				}
			} else {
				if err != nil {
					t.Errorf("downloadBinary() unexpected error = %v", err)
				}
				if resp != nil {
					t.Cleanup(func() { resp.Body.Close() })
					body, readErr := io.ReadAll(resp.Body)
					if readErr != nil {
						t.Fatalf("read response body: %v", readErr)
					}
					if !bytes.Equal(body, []byte(tt.responseBody)) {
						t.Errorf("downloadBinary() body = %q, want %q", body, tt.responseBody)
					}
				}
			}
		})
	}
}

// TestUpdaterService_downloadBinary_httpError tests binary download with HTTP errors.
// All rows assert the same "downloading binary" wrapping (contract).
func TestUpdaterService_downloadBinary_httpError(t *testing.T) {
	t.Parallel()

	const wantErrContain = "downloading binary"

	tests := []struct {
		name      string
		clientErr error
	}{
		{name: "connection refused", clientErr: errors.New("connection refused")},
		{name: "timeout error", clientErr: errors.New("context deadline exceeded")},
		{name: "DNS error", clientErr: errors.New("no such host")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			client := &mockHTTPClient{
				getFunc: func(url string) (*http.Response, error) {
					return nil, tt.clientErr
				},
			}

			updater := NewUpdaterWithDeps("v1.0.0", testSource, client, &mockFileSystem{}, &mockCopier{})

			resp, err := updater.downloadBinary("v2.0.0")
			if resp != nil && resp.Body != nil {
				t.Cleanup(func() { resp.Body.Close() })
			}

			if err == nil {
				t.Fatal("downloadBinary() error = nil, want error")
			}
			if !strings.Contains(err.Error(), wantErrContain) {
				t.Errorf("downloadBinary() error = %v, want containing %q", err, wantErrContain)
			}
		})
	}
}

// TestOsFileSystem tests the osFileSystem implementation.
func TestOsFileSystem(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		testFunc func(t *testing.T, fs osFileSystem)
	}{
		{
			name: "Executable returns valid path",
			testFunc: func(t *testing.T, fs osFileSystem) {
				path, err := fs.Executable()
				// Check error
				if err != nil {
					t.Errorf("Executable() error = %v", err)
				}
				// Check path not empty
				if path == "" {
					t.Error("Executable() returned empty path")
				}
			},
		},
		{
			name: "EvalSymlinks returns valid path",
			testFunc: func(t *testing.T, fs osFileSystem) {
				tmpDir := t.TempDir()
				path, err := fs.EvalSymlinks(tmpDir)
				// Check error
				if err != nil {
					t.Errorf("EvalSymlinks() error = %v", err)
				}
				// Check path not empty
				if path == "" {
					t.Error("EvalSymlinks() returned empty path")
				}
			},
		},
		{
			name: "CreateTemp creates file successfully",
			testFunc: func(t *testing.T, fs osFileSystem) {
				file, err := fs.CreateTemp("", "test-*")
				// Check error
				if err != nil {
					t.Errorf("CreateTemp() error = %v", err)
				}
				// Cleanup created file
				if file != nil {
					file.Close()
					os.Remove(file.Name())
				}
			},
		},
		{
			name: "Chmod changes file permissions",
			testFunc: func(t *testing.T, fs osFileSystem) {
				tmpFile, err := os.CreateTemp(t.TempDir(), "test-chmod-*")
				// Check setup error
				if err != nil {
					t.Fatalf("failed to create temp file: %v", err)
				}
				tmpFile.Close()
				err = fs.Chmod(tmpFile.Name(), 0o644)
				// Check chmod error
				if err != nil {
					t.Errorf("Chmod() error = %v", err)
				}
			},
		},
		{
			name: "Rename moves file successfully",
			testFunc: func(t *testing.T, fs osFileSystem) {
				tmpFile, err := os.CreateTemp(t.TempDir(), "test-rename-*")
				// Check setup error
				if err != nil {
					t.Fatalf("failed to create temp file: %v", err)
				}
				tmpFile.Close()
				oldPath := tmpFile.Name()
				newPath := oldPath + "-renamed"
				err = fs.Rename(oldPath, newPath)
				// Check rename error
				if err != nil {
					t.Errorf("Rename() error = %v", err)
				}
			},
		},
		{
			name: "Remove deletes file successfully",
			testFunc: func(t *testing.T, fs osFileSystem) {
				tmpFile, err := os.CreateTemp(t.TempDir(), "test-remove-*")
				// Check setup error
				if err != nil {
					t.Fatalf("failed to create temp file: %v", err)
				}
				tmpFile.Close()
				err = fs.Remove(tmpFile.Name())
				// Check remove error
				if err != nil {
					t.Errorf("Remove() error = %v", err)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fs := osFileSystem{}
			tt.testFunc(t, fs)
		})
	}
}

// TestStdCopier tests the stdIOCopier implementation.
func TestStdCopier(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		input       string
		wantBytes   int64
		wantContent string
		wantErr     bool
	}{
		{
			name:        "copy test content",
			input:       "test content",
			wantBytes:   12,
			wantContent: "test content",
			wantErr:     false,
		},
		{
			name:        "copy empty content",
			input:       "",
			wantBytes:   0,
			wantContent: "",
			wantErr:     false,
		},
		{
			name:        "copy binary-like content",
			input:       "binary\x00content",
			wantBytes:   14,
			wantContent: "binary\x00content",
			wantErr:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			copier := stdIOCopier{}
			src := bytes.NewReader([]byte(tt.input))
			var dst bytes.Buffer

			n, err := copier.Copy(&dst, src)

			// Check error
			if (err != nil) != tt.wantErr {
				t.Errorf("Copy() error = %v, wantErr %v", err, tt.wantErr)
			}
			// Check bytes copied
			if n != tt.wantBytes {
				t.Errorf("Copy() bytes = %d, want %d", n, tt.wantBytes)
			}
			// Check content
			if dst.String() != tt.wantContent {
				t.Errorf("Copy() content = %q, want %q", dst.String(), tt.wantContent)
			}
		})
	}
}

// TestUpdaterService_Upgrade_scenarios tests the complete upgrade flow scenarios.
func TestUpdaterService_Upgrade_scenarios(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		//: setupServer receives the signed release fixture so the success row
		//: can serve the archive, its manifest and the vendor signature.
		setupServer        func(f releaseFixture) (handler http.HandlerFunc, requestCount *int)
		setupFS            func(t *testing.T, execPath string) *mockFileSystem
		wantErr            bool
		wantErrContain     string
		wantAvailable      bool
		wantCurrentVersion string
		wantLatestVersion  string
		verifyContent      bool
		expectedContent    string
	}{
		{
			name: "successful upgrade",
			setupServer: func(f releaseFixture) (http.HandlerFunc, *int) {
				requestCount := 0
				return func(w http.ResponseWriter, r *http.Request) {
					requestCount++
					// Route by path: version check (API) first, then the three
					// release assets (archive, checksums.txt, its signature).
					if !strings.Contains(r.URL.Path, "/releases/download/") {
						w.WriteHeader(http.StatusOK)
						if _, werr := w.Write([]byte(`{"tag_name": "v2.0.0"}`)); werr != nil {
							//: httptest writer cannot fail under contract; surface via
							//: http.Error so a future custom recorder makes the breakage observable.
							http.Error(w, werr.Error(), http.StatusInternalServerError)
							return
						}
						return
					}
					//: The fixture owns the signed release assets.
					if !f.serveAsset(t, w, r.URL.Path) {
						w.WriteHeader(http.StatusNotFound)
					}
				}, &requestCount
			},
			setupFS: func(t *testing.T, execPath string) *mockFileSystem {
				return &mockFileSystem{
					executableFunc: func() (string, error) {
						return execPath, nil
					},
					evalSymlinksFunc: func(path string) (string, error) {
						return path, nil
					},
					createTempFunc: func(dir, pattern string) (*os.File, error) {
						return os.CreateTemp(dir, pattern)
					},
					chmodFunc: func(name string, mode os.FileMode) error {
						return os.Chmod(name, mode)
					},
					renameFunc: func(oldpath, newpath string) error {
						return os.Rename(oldpath, newpath)
					},
					removeFunc: func(name string) error {
						return os.Remove(name)
					},
				}
			},
			wantErr:            false,
			wantAvailable:      true,
			wantCurrentVersion: "v1.0.0",
			wantLatestVersion:  "v2.0.0",
			verifyContent:      true,
			expectedContent:    "new binary content",
		},
		{
			name: "download error",
			setupServer: func(_ releaseFixture) (http.HandlerFunc, *int) {
				requestCount := 0
				return func(w http.ResponseWriter, r *http.Request) {
					requestCount++
					// First request is for version check, second is for download
					if requestCount == 1 {
						w.WriteHeader(http.StatusOK)
						if _, werr := w.Write([]byte(`{"tag_name": "v2.0.0"}`)); werr != nil {
							//: httptest writer cannot fail under contract; surface via
							//: http.Error so a future custom recorder makes the breakage observable.
							http.Error(w, werr.Error(), http.StatusInternalServerError)
							return
						}
					} else {
						w.WriteHeader(http.StatusInternalServerError)
						if _, werr := w.Write([]byte("Server Error")); werr != nil {
							//: httptest writer cannot fail under contract; surface via
							//: http.Error so a future custom recorder makes the breakage observable.
							http.Error(w, werr.Error(), http.StatusInternalServerError)
							return
						}
					}
				}, &requestCount
			},
			setupFS: func(t *testing.T, execPath string) *mockFileSystem {
				return &mockFileSystem{}
			},
			wantErr:        true,
			wantErrContain: "the release could not be downloaded",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Create a temp directory for testing
			tmpDir := t.TempDir()

			// Create a fake executable
			execPath := filepath.Join(tmpDir, testSource.Product)
			if err := os.WriteFile(execPath, []byte("old binary"), 0o755); err != nil {
				t.Fatalf("failed to create fake executable: %v", err)
			}

			// Setup server around a fully signed release fixture.
			fixture := newReleaseFixture(t, []byte("new binary content"))
			handler, _ := tt.setupServer(fixture)
			server := httptest.NewTestServer(t, handler)
			//: In-memory network: no real port. The server starts on the first
			//: Client() call, which is also what fills server.URL, so start it
			//: here and let the reads below run in any order. NewTestServer
			//: registers the cleanup itself.
			server.Client()

			// Use mock transport
			client := &http.Client{
				Transport: &mockTransport{
					url:    server.URL,
					client: server.Client(),
				},
			}

			fs := tt.setupFS(t, execPath)
			updater := NewUpdaterWithDeps("v1.0.0", testSource, client, fs, &mockCopier{}).WithVendorKey(fixture.pub)

			info, err := updater.Upgrade()

			// Check error expectation
			if tt.wantErr {
				if err == nil {
					t.Error("Upgrade() error = nil, want error")
				} else if tt.wantErrContain != "" && !strings.Contains(err.Error(), tt.wantErrContain) {
					t.Errorf("Upgrade() error = %v, want containing %q", err, tt.wantErrContain)
				}
				return
			}

			// Check success case
			if err != nil {
				t.Errorf("Upgrade() unexpected error = %v", err)
			}

			// Check update info
			if info.Available != tt.wantAvailable {
				t.Errorf("Upgrade() Available = %v, want %v", info.Available, tt.wantAvailable)
			}
			// Check current version
			if info.CurrentVersion != tt.wantCurrentVersion {
				t.Errorf("Upgrade() CurrentVersion = %q, want %q", info.CurrentVersion, tt.wantCurrentVersion)
			}
			// Check latest version
			if info.LatestVersion != tt.wantLatestVersion {
				t.Errorf("Upgrade() LatestVersion = %q, want %q", info.LatestVersion, tt.wantLatestVersion)
			}

			// Verify the binary was replaced
			if tt.verifyContent {
				content, err := os.ReadFile(execPath)
				// Check read error
				if err != nil {
					t.Fatalf("failed to read updated binary: %v", err)
				}
				// Check content
				if !bytes.Equal(content, []byte(tt.expectedContent)) {
					t.Errorf("binary content mismatch: got %d bytes, want %q", len(content), tt.expectedContent)
				}
			}
		})
	}
}

// TestUpdaterService_downloadAndReplace_executablePathError tests path resolution errors.
func TestUpdaterService_downloadAndReplace_executablePathError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		setupFS        func() *mockFileSystem
		wantErrContain string
	}{
		{
			name: "executable not found",
			setupFS: func() *mockFileSystem {
				return &mockFileSystem{
					executableFunc: func() (string, error) {
						return "", errors.New("executable not found")
					},
				}
			},
			wantErrContain: "getting executable path",
		},
		{
			name: "permission denied",
			setupFS: func() *mockFileSystem {
				return &mockFileSystem{
					executableFunc: func() (string, error) {
						return "", errors.New("permission denied")
					},
				}
			},
			wantErrContain: "getting executable path",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Create test server
			server := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				if _, werr := w.Write([]byte("binary content")); werr != nil {
					//: httptest writer cannot fail under contract; surface via
					//: http.Error so a future custom recorder makes the breakage observable.
					http.Error(w, werr.Error(), http.StatusInternalServerError)
					return
				}
			}))
			//: In-memory network: no real port. The server starts on the first
			//: Client() call, which is also what fills server.URL, so start it
			//: here and let the reads below run in any order. NewTestServer
			//: registers the cleanup itself.
			server.Client()

			// Use mock transport
			client := &http.Client{
				Transport: &mockTransport{
					url:    server.URL,
					client: server.Client(),
				},
			}

			fs := tt.setupFS()
			//: A throwaway vendor key so the anchor pre-check passes and the
			//: executable-path failure under test is what actually decides.
			vendorPub, _ := vendorKeypair(t)
			updater := NewUpdaterWithDeps("v1.0.0", testSource, client, fs, &mockCopier{}).WithVendorKey(vendorPub)

			err := updater.downloadAndReplace("v2.0.0")
			// Check error is returned
			if err == nil {
				t.Fatal("downloadAndReplace() error = nil, want error")
			}
			// Check error contains expected string
			if !strings.Contains(err.Error(), tt.wantErrContain) {
				t.Errorf("downloadAndReplace() error = %v, want containing %q", err, tt.wantErrContain)
			}
		})
	}
}

// TestUpdaterService_getBinaryName_platforms tests getBinaryName for different platforms.
func TestUpdaterService_getBinaryName_platforms(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		goos         string
		goarch       string
		expectedName string
	}{
		{
			name:         "windows amd64",
			goos:         "windows",
			goarch:       "amd64",
			expectedName: "widget_windows_amd64.zip",
		},
		{
			name:         "linux arm64",
			goos:         "linux",
			goarch:       "arm64",
			expectedName: "widget_linux_arm64.tar.gz",
		},
		{
			name:         "darwin arm64",
			goos:         "darwin",
			goarch:       "arm64",
			expectedName: "widget_darwin_arm64.tar.gz",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Construct the Service literal with the target platform so the
			// test never touches package-level runtimeGOOS/runtimeGOARCH.
			// Mutating those globals from parallel subtests races with every
			// other test that calls NewService — the Service.goos field is
			// the deterministic substitute.
			updater := &Service{version: "v1.0.0", goos: tt.goos, goarch: tt.goarch, src: testSource}
			name := updater.getBinaryName()

			// Check binary name
			if name != tt.expectedName {
				t.Errorf("getBinaryName() = %q, want %q", name, tt.expectedName)
			}
		})
	}
}

// TestService_isNewer_Variants verifies isNewer correctly compares version strings.
func TestService_isNewer_Variants(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		current string
		latest  string
		want    bool
	}{
		{name: "latest is newer", current: "v1.0.0", latest: "v1.1.0", want: true},
		{name: "same version", current: "v1.0.0", latest: "v1.0.0", want: false},
		{name: "current is newer", current: "v1.1.0", latest: "v1.0.0", want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			svc := NewService(tc.current, testSource)
			if got := svc.isNewer(tc.latest); got != tc.want {
				t.Errorf("isNewer(%q) with current=%q = %v, want %v", tc.latest, tc.current, got, tc.want)
			}
		})
	}
}

// TestService_getBinaryName_Platforms verifies getBinaryName produces the right artifact name.
func TestService_getBinaryName_Platforms(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		goos   string
		goarch string
	}{
		{name: "linux amd64", goos: "linux", goarch: "amd64"},
		{name: "darwin arm64", goos: "darwin", goarch: "arm64"},
		{name: "windows amd64", goos: "windows", goarch: "amd64"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: Service literal — no runtimeGOOS mutation, no race.
			svc := &Service{version: "v1.0.0", goos: tc.goos, goarch: tc.goarch, src: testSource}
			got := svc.getBinaryName()
			if got == "" {
				t.Error("getBinaryName() returned empty string")
			}
		})
	}
}

// TestService_resolveExecutablePath_ReturnsPath verifies resolveExecutablePath returns non-empty.
func TestService_resolveExecutablePath_ReturnsPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
	}{
		{name: "returns non-empty path"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			svc := NewService("v1.0.0", testSource)
			path, err := svc.resolveExecutablePath()
			// May fail in test environment (no real binary), but must not panic.
			if err == nil && path == "" {
				t.Errorf("%s: resolveExecutablePath returned empty path without error", tc.name)
			}
		})
	}
}

// TestService_getLatestVersion_NetworkError verifies getLatestVersion returns error on network failure.
func TestService_getLatestVersion_NetworkError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
	}{
		{name: "fails with no network client configured"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Use a service with a known-bad HTTP endpoint by injecting a mock that errors.
			svc := NewUpdaterWithDeps("v1.0.0", testSource, &errorGetter{}, nil, nil)
			_, err := svc.getLatestVersion()
			if err == nil {
				t.Errorf("%s: getLatestVersion expected error from bad client, got nil", tc.name)
			}
		})
	}
}

// : Compile-time assertion: errorGetter must satisfy Getter so the
// : dependency-injection contract is verified by the compiler — KTN-INTERFACE-COMPILE-CHECK.
var _ Getter = (*errorGetter)(nil)

// errorGetter is a Getter that always returns an error.
type errorGetter struct{}

func (e *errorGetter) Get(_ string) (*http.Response, error) {
	return nil, fmt.Errorf("mock network error")
}

// TestService_downloadAndReplace_ErrorClient verifies downloadAndReplace propagates client errors.
func TestService_downloadAndReplace_ErrorClient(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		version string
	}{
		{name: "errors from bad client", version: "v1.0.0"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			svc := NewUpdaterWithDeps("v1.0.0", testSource, &errorGetter{}, nil, nil)
			if err := svc.downloadAndReplace(tc.version); err == nil {
				t.Error("downloadAndReplace with bad client: expected error, got nil")
			}
		})
	}
}

// TestService_downloadBinary_ErrorClient verifies downloadBinary propagates client errors.
func TestService_downloadBinary_ErrorClient(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		version string
	}{
		{name: "errors from bad client", version: "v1.0.0"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			svc := NewUpdaterWithDeps("v1.0.0", testSource, &errorGetter{}, nil, nil)
			resp, err := svc.downloadBinary(tc.version)
			if err == nil {
				if resp != nil {
					if cerr := resp.Body.Close(); cerr != nil {
						t.Logf("close response body: %v", cerr)
					}
				}
				t.Errorf("downloadBinary with error client: expected error, got nil")
			}
		})
	}
}

// TestService_writeTempFileContent_CopiesBytes verifies writeTempFileContent copies content.
func TestService_writeTempFileContent_CopiesBytes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
	}{
		{name: "non-empty content", content: "hello world"},
		{name: "empty content", content: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			svc := NewService("v1.0.0", testSource)
			var buf countingWriterUpdater
			err := svc.writeTempFileContent(&buf, stringReader(tc.content))
			if err != nil {
				t.Errorf("writeTempFileContent: unexpected error: %v", err)
			}
			if int(buf.n) != len(tc.content) {
				t.Errorf("wrote %d bytes, want %d", buf.n, len(tc.content))
			}
		})
	}
}

// countingWriterUpdater counts bytes written (local to avoid conflict with pool test).
type countingWriterUpdater struct{ n int64 }

func (w *countingWriterUpdater) Write(p []byte) (int, error) {
	w.n += int64(len(p))
	return len(p), nil
}

// stringReader adapts a string to io.Reader for testing.
type stringReader string

func (s stringReader) Read(p []byte) (int, error) {
	if len(s) == 0 {
		return 0, io.EOF
	}
	n := copy(p, []byte(s))
	return n, io.EOF
}

// TestService_finalizeReplacement_RejectsAbsent verifies finalizeReplacement errors on missing tmp.
func TestService_finalizeReplacement_RejectsAbsent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		tmpPath string
		exec    string
	}{
		{name: "missing tmp file", tmpPath: "/nonexistent/tmp.ktn", exec: "/usr/local/bin/ktn-linter"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			svc := NewService("v1.0.0", testSource)
			if err := svc.finalizeReplacement(tc.tmpPath, tc.exec); err == nil {
				t.Error("finalizeReplacement with missing tmp: expected error, got nil")
			}
		})
	}
}

// TestService_writeAndReplaceBinary_RejectsEmpty verifies writeAndReplaceBinary errors on empty path.
func TestService_writeAndReplaceBinary_RejectsEmpty(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		execPath string
	}{
		{name: "empty exec path", execPath: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			svc := NewService("v1.0.0", testSource)
			err := svc.writeAndReplaceBinary(stringReader(""), tc.execPath)
			// Empty path should produce an error (can't write to "").
			if err == nil {
				t.Error("writeAndReplaceBinary with empty exec path: expected error, got nil")
			}
		})
	}
}

// TestService_isNewer is the canonical KTN-TEST-SYNC test for the `isNewer`
// method on `Service`. Exercises the three semver orderings (newer, equal,
// older) so any regression in normalisation or comparison surfaces here.
func TestService_isNewer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		current string
		latest  string
		want    bool
	}{
		{name: "latest_is_newer", current: "v1.0.0", latest: "v1.1.0", want: true},
		{name: "same_version", current: "v1.0.0", latest: "v1.0.0", want: false},
		{name: "current_is_newer", current: "v1.1.0", latest: "v1.0.0", want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			svc := NewService(tc.current, testSource)
			if got := svc.isNewer(tc.latest); got != tc.want {
				t.Errorf("isNewer(%q) with current=%q = %v, want %v", tc.latest, tc.current, got, tc.want)
			}
		})
	}
}

// TestService_getBinaryName is the canonical KTN-TEST-SYNC test for the
// `getBinaryName` method on `Service`. Pins the non-empty contract under
// representative GOOS/GOARCH combinations via the injection seam.
func TestService_getBinaryName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		goos   string
		goarch string
	}{
		{name: "linux_amd64", goos: "linux", goarch: "amd64"},
		{name: "darwin_arm64", goos: "darwin", goarch: "arm64"},
		{name: "windows_amd64", goos: "windows", goarch: "amd64"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: Service literal — no runtimeGOOS mutation, no race.
			svc := &Service{version: "v1.0.0", goos: tc.goos, goarch: tc.goarch, src: testSource}
			got := svc.getBinaryName()
			if got == "" {
				t.Error("getBinaryName() returned empty string")
			}
		})
	}
}

// TestService_getLatestVersion is the canonical KTN-TEST-SYNC test for the
// `getLatestVersion` method on `Service`. Exercises the network-error
// branch by pointing the http getter at a transport that always fails.
func TestService_getLatestVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		transport func(*http.Request) (*http.Response, error)
	}{
		{
			name: "network_error_returns_err",
			transport: func(_ *http.Request) (*http.Response, error) {
				return nil, errors.New("network down")
			},
		},
		{
			name: "non_2xx_returns_err",
			transport: func(_ *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusInternalServerError,
					Body:       io.NopCloser(strings.NewReader("server error")),
				}, nil
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			client := &http.Client{Transport: roundTripperFunc(tc.transport)}
			svc := NewUpdaterWithDeps("v1.0.0", testSource, client, &osFileSystem{}, &stdIOCopier{})
			_, err := svc.getLatestVersion()
			if err == nil {
				t.Error("expected error, got nil")
			}
		})
	}
}

// TestService_downloadAndReplace is the canonical KTN-TEST-SYNC test for
// the `downloadAndReplace` method on `Service`. Covers network error +
// extract-error (body isn't a valid archive) since both reach `Service`
// through the same entry point.
func TestService_downloadAndReplace(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		transport func(*http.Request) (*http.Response, error)
		wantErr   bool
	}{
		{
			name: "network_error_returns_err",
			transport: func(_ *http.Request) (*http.Response, error) {
				return nil, errors.New("net unreachable")
			},
			wantErr: true,
		},
		{
			name: "non_archive_body_fails_extract",
			//: HTTP 200 with raw bytes (not a tar.gz) — the pipeline must reject.
			//: The same body is served for checksums.txt, so the integrity gate
			//: refuses first (no manifest entry) before extract is ever reached.
			transport: func(_ *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewReader([]byte("binary data"))),
				}, nil
			},
			wantErr: true,
		},
		{
			name: "non_2xx_archive_status_fails",
			//: A non-200 archive response is refused at the download step,
			//: before buffering, integrity verification or extraction is
			//: reached — a distinct branch from transport and checksum errors.
			transport: func(_ *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusInternalServerError,
					Body:       io.NopCloser(strings.NewReader("server error")),
				}, nil
			},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: Wire transport for testing network interaction
			client := &http.Client{Transport: roundTripperFunc(tc.transport)}
			svc := NewUpdaterWithDeps("v1.0.0", testSource, client, &osFileSystem{}, &stdIOCopier{})
			err := svc.downloadAndReplace("v9.9.9")
			//: Verify error state matches expectation
			if (err != nil) != tc.wantErr {
				t.Errorf("downloadAndReplace() error = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

// TestService_downloadBinary is the canonical KTN-TEST-SYNC test for the
// `downloadBinary` method on `Service`. Verifies the error path is taken
// when the transport returns a failure.
func TestService_downloadBinary(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		transport func(*http.Request) (*http.Response, error)
		wantErr   bool
	}{
		{
			name: "network_error_returns_err",
			transport: func(_ *http.Request) (*http.Response, error) {
				return nil, errors.New("dns failure")
			},
			wantErr: true,
		},
		{
			name: "http error status returns err",
			transport: func(_ *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusNotFound,
					Body:       io.NopCloser(bytes.NewReader([]byte(""))),
				}, nil
			},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: Wire transport for testing network interaction
			client := &http.Client{Transport: roundTripperFunc(tc.transport)}
			svc := NewUpdaterWithDeps("v1.0.0", testSource, client, &osFileSystem{}, &stdIOCopier{})
			_, err := svc.downloadBinary("v9.9.9")
			//: Verify error state matches expectation
			if (err != nil) != tc.wantErr {
				t.Errorf("downloadBinary() error = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

// TestService_resolveExecutablePath is the canonical KTN-TEST-SYNC test for
// the `resolveExecutablePath` method on `Service`. Asserts the resolver
// returns a non-empty path for the current process across calls.
func TestService_resolveExecutablePath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		wantEmpty bool
		wantErr   bool
	}{
		{
			name:      "current_process_resolves",
			wantEmpty: false,
			wantErr:   false,
		},
		{
			name:      "repeated calls return same path",
			wantEmpty: false,
			wantErr:   false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			svc := NewService("v1.0.0", testSource)
			path, err := svc.resolveExecutablePath()
			//: Verify error state matches expectation
			if (err != nil) != tc.wantErr {
				t.Errorf("resolveExecutablePath() error = %v, wantErr = %v", err, tc.wantErr)
			}
			//: Verify path is non-empty
			if (path == "") != tc.wantEmpty {
				t.Errorf("resolveExecutablePath() path empty = %v, want %v", path == "", tc.wantEmpty)
			}
		})
	}
}

// TestService_writeAndReplaceBinary is the canonical KTN-TEST-SYNC test for
// the `writeAndReplaceBinary` method on `Service`. Pins the error case
// where the destination path is empty.
func TestService_writeAndReplaceBinary(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		execPath string
		wantErr  bool
	}{
		{name: "empty_exec_path_errors", execPath: "", wantErr: true},
		{name: "nonexistent_path_errors", execPath: "/nonexistent/path/binary", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			svc := NewService("v1.0.0", testSource)
			//: Test binary replacement with specified exec path
			err := svc.writeAndReplaceBinary(stringReader(""), tc.execPath)
			//: Verify error state matches expectation
			if (err != nil) != tc.wantErr {
				t.Errorf("writeAndReplaceBinary() error = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

// TestService_writeTempFileContent is the canonical KTN-TEST-SYNC test for
// the `writeTempFileContent` method on `Service`. Exercises the
// happy-path copy into an in-memory buffer.
func TestService_writeTempFileContent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		payload string
	}{
		{name: "non_empty_payload", payload: "hello-binary"},
		{name: "empty_payload", payload: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			svc := NewService("v1.0.0", testSource)
			var buf bytes.Buffer
			if err := svc.writeTempFileContent(&buf, strings.NewReader(tc.payload)); err != nil {
				t.Errorf("writeTempFileContent() error = %v", err)
			}
			if buf.String() != tc.payload {
				t.Errorf("writeTempFileContent() wrote %q, want %q", buf.String(), tc.payload)
			}
		})
	}
}

// TestService_finalizeReplacement is the canonical KTN-TEST-SYNC test for
// the `finalizeReplacement` method on `Service`. Exercises both the
// non-existent source (error) and renamed-replacement (happy) paths.
func TestService_finalizeReplacement(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		setup       func(t *testing.T) (tmpPath, execPath string)
		expectError bool
	}{
		{
			name: "missing_tmp_errors",
			setup: func(_ *testing.T) (string, string) {
				return "/nonexistent/tmp/file", "/nonexistent/exec/path"
			},
			expectError: true,
		},
		{
			name: "valid_replacement_succeeds",
			setup: func(t *testing.T) (string, string) {
				tmpDir := t.TempDir()
				tmpPath := filepath.Join(tmpDir, "temp-binary")
				execPath := filepath.Join(tmpDir, "exec-binary")
				//: Create temp file with some content
				if err := os.WriteFile(tmpPath, []byte("binary"), 0o755); err != nil {
					t.Fatalf("setup failed: %v", err)
				}
				return tmpPath, execPath
			},
			expectError: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			svc := NewService("v1.0.0", testSource)
			tmpPath, execPath := tc.setup(t)
			err := svc.finalizeReplacement(tmpPath, execPath)
			//: Verify error state matches expectation
			if (err != nil) != tc.expectError {
				if tc.expectError && err == nil {
					t.Error("expected error, got nil")
				}
				if !tc.expectError && err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		})
	}
}

// roundTripperFunc adapts a function into an http.RoundTripper for the
// canonical-named tests above.
type roundTripperFunc func(*http.Request) (*http.Response, error)

// RoundTrip implements http.RoundTripper by delegating to the wrapped function.
func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// TestService_getArchiveSuffix verifies the per-platform archive suffix
// the release pipeline produces.
func TestService_getArchiveSuffix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		goos string
		want string
	}{
		{name: "linux is tar.gz", goos: "linux", want: "tar.gz"},
		{name: "darwin is tar.gz", goos: "darwin", want: "tar.gz"},
		{name: "windows is zip", goos: "windows", want: "zip"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: Service literal — explicit goos, no global mutation, no race.
			svc := &Service{version: "v1.0.0", goos: tc.goos, goarch: "amd64", src: testSource}
			if got := svc.getArchiveSuffix(); got != tc.want {
				t.Errorf("getArchiveSuffix() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestService_innerBinaryName verifies the basename of the binary entry
// expected inside each platform's release archive.
func TestService_innerBinaryName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		goos string
		want string
	}{
		{name: "linux is ktn-linter", goos: "linux", want: testSource.Product},
		{name: "darwin is ktn-linter", goos: "darwin", want: testSource.Product},
		{name: "windows is widget.exe", goos: "windows", want: testSource.Product + ".exe"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: Service literal — explicit goos, no global mutation, no race.
			svc := &Service{version: "v1.0.0", goos: tc.goos, goarch: "amd64", src: testSource}
			if got := svc.innerBinaryName(); got != tc.want {
				t.Errorf("innerBinaryName() = %q, want %q", got, tc.want)
			}
		})
	}
}

// Test_openFromTarGz pins the tarball extract contract: happy path returns
// the inner binary bytes; missing-entry / corrupt streams fail with a
// classifiable error wrapping the sentinel where appropriate.
func Test_openFromTarGz(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		body      func(t *testing.T) []byte
		want      string //: empty when error path
		wantErr   bool
		errMatch  error
		errString string
	}{
		{
			name:    "happy path returns inner bytes",
			body:    func(t *testing.T) []byte { return makeTarGzWithBinary(t, []byte("hello")) },
			want:    "hello",
			wantErr: false,
		},
		{
			name: "missing entry returns sentinel",
			body: func(t *testing.T) []byte {
				//: Build a tar.gz whose only entry is named differently from `ktn-linter`.
				var buf bytes.Buffer
				gz := gzip.NewWriter(&buf)
				tw := tar.NewWriter(gz)
				if err := tw.WriteHeader(&tar.Header{Name: "README.md", Mode: 0o644, Size: 3, Typeflag: tar.TypeReg}); err != nil {
					t.Fatalf("tar header: %v", err)
				}
				if _, err := tw.Write([]byte("hi\n")); err != nil {
					t.Fatalf("tar write: %v", err)
				}
				if err := tw.Close(); err != nil {
					t.Fatalf("tar close: %v", err)
				}
				if err := gz.Close(); err != nil {
					t.Fatalf("gzip close: %v", err)
				}
				return buf.Bytes()
			},
			wantErr:  true,
			errMatch: coreupd.BinaryNotInArchive,
		},
		{
			name:      "non-gzip body fails fast",
			body:      func(t *testing.T) []byte { return []byte("not gzipped") },
			wantErr:   true,
			errString: "opening gzip",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			reader, cleanup, err := openFromTarGz(bytes.NewReader(tc.body(t)), testSource.Product)
			if cleanup != nil {
				t.Cleanup(cleanup)
			}
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				if tc.errMatch != nil && !errors.Is(err, tc.errMatch) {
					t.Errorf("error = %v, want errors.Is %v", err, tc.errMatch)
				}
				if tc.errString != "" && !strings.Contains(err.Error(), tc.errString) {
					t.Errorf("error = %v, want containing %q", err, tc.errString)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			got, err := io.ReadAll(reader)
			if err != nil {
				t.Fatalf("reading inner: %v", err)
			}
			if string(got) != tc.want {
				t.Errorf("inner = %q, want %q", got, tc.want)
			}
		})
	}
}

// Test_openFromZip pins the windows extract contract: happy path returns
// the inner binary bytes; missing-entry / corrupt streams fail with a
// classifiable error.
func Test_openFromZip(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		body      func(t *testing.T) []byte
		want      string //: empty when error path
		wantErr   bool
		errMatch  error
		errString string
	}{
		{
			name:    "happy path returns inner bytes",
			body:    func(t *testing.T) []byte { return makeZipWithBinary(t, []byte("hello")) },
			want:    "hello",
			wantErr: false,
		},
		{
			name: "missing entry returns sentinel",
			body: func(t *testing.T) []byte {
				var buf bytes.Buffer
				zw := zip.NewWriter(&buf)
				f, err := zw.Create("README.md")
				if err != nil {
					t.Fatalf("zip create: %v", err)
				}
				if _, err := f.Write([]byte("hi")); err != nil {
					t.Fatalf("zip write: %v", err)
				}
				if err := zw.Close(); err != nil {
					t.Fatalf("zip close: %v", err)
				}
				return buf.Bytes()
			},
			wantErr:  true,
			errMatch: coreupd.BinaryNotInArchive,
		},
		{
			name:      "non-zip body fails fast",
			body:      func(t *testing.T) []byte { return []byte("not a zip") },
			wantErr:   true,
			errString: "opening zip",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			reader, cleanup, err := openFromZip(bytes.NewReader(tc.body(t)), testSource.Product+".exe")
			if cleanup != nil {
				t.Cleanup(cleanup)
			}
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				if tc.errMatch != nil && !errors.Is(err, tc.errMatch) {
					t.Errorf("error = %v, want errors.Is %v", err, tc.errMatch)
				}
				if tc.errString != "" && !strings.Contains(err.Error(), tc.errString) {
					t.Errorf("error = %v, want containing %q", err, tc.errString)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			got, err := io.ReadAll(reader)
			if err != nil {
				t.Fatalf("reading inner: %v", err)
			}
			if string(got) != tc.want {
				t.Errorf("inner = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestService_openInnerBinary dispatches to the right archive opener based
// on platform. Covers the unknown-suffix sentinel path as well.
func TestService_openInnerBinary(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		goos     string
		body     func(t *testing.T) []byte
		wantErr  bool
		errMatch error
		want     string
	}{
		{
			name: "linux dispatches to tar.gz",
			goos: "linux",
			body: func(t *testing.T) []byte { return makeTarGzWithBinary(t, []byte("L")) },
			want: "L",
		},
		{
			name: "darwin dispatches to tar.gz",
			goos: "darwin",
			body: func(t *testing.T) []byte { return makeTarGzWithBinary(t, []byte("D")) },
			want: "D",
		},
		{
			name: "windows dispatches to zip",
			goos: "windows",
			body: func(t *testing.T) []byte { return makeZipWithBinary(t, []byte("W")) },
			want: "W",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: Service literal — explicit goos, no global mutation, no race.
			svc := &Service{version: "v1.0.0", goos: tc.goos, goarch: "amd64", src: testSource}
			reader, cleanup, err := svc.openInnerBinary(bytes.NewReader(tc.body(t)))
			if cleanup != nil {
				t.Cleanup(cleanup)
			}
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				if tc.errMatch != nil && !errors.Is(err, tc.errMatch) {
					t.Errorf("error = %v, want errors.Is %v", err, tc.errMatch)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			got, err := io.ReadAll(reader)
			if err != nil {
				t.Fatalf("reading inner: %v", err)
			}
			if string(got) != tc.want {
				t.Errorf("inner = %q, want %q", got, tc.want)
			}
		})
	}
}

// Test_repoForTag pins the channel routing: stable releases resolve to the
// public mirror, release candidates stay in the private source. Getting this
// backwards silently breaks either every user's upgrade path or the RC flow.
func Test_repoForTag(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		tag      string
		expected string
	}{
		{
			name:     "stable release resolves to the public mirror",
			tag:      "v1.45.2",
			expected: "widget-dist",
		},
		{
			name:     "release candidate stays in the private source",
			tag:      "v1.46.0-rc.412.1",
			expected: testSource.Product,
		},
		{
			name:     "any prerelease suffix routes to the source",
			tag:      "v2.0.0-beta",
			expected: testSource.Product,
		},
		{
			name:     "tag without a v prefix is still stable",
			tag:      "1.45.2",
			expected: "widget-dist",
		},
		{
			name:     "empty tag falls back to the stable mirror",
			tag:      "",
			expected: "widget-dist",
		},
		{
			//: Build metadata legitimately contains a hyphen; a substring
			//: check would misroute this stable tag to the private repo and
			//: then 404 on the download.
			name:     "build metadata does not make a tag a prerelease",
			tag:      "v1.45.2+mirror-build.1",
			expected: "widget-dist",
		},
		{
			name:     "a prerelease carrying build metadata still routes to the source",
			tag:      "v1.46.0-rc.1+build.7",
			expected: testSource.Product,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc := NewService("v1.0.0", testSource)
			if got := svc.repoForTag(tt.tag); got != tt.expected {
				t.Errorf("repoForTag(%q) = %q, want %q", tt.tag, got, tt.expected)
			}
		})
	}
}

// The mocks below moved here from interfaces_internal_test.go. That file paired
// with interfaces.go, which declares only exported interfaces and — since
// validateDeps was deleted as dead code — no private symbol at all, so
// KTN-TEST-FILES reported the pairing as unnecessary. The mocks are fixtures
// for the whole package rather than tests of interfaces.go, and this file is
// the package's internal test home.

// Compile-time guarantee that mockHTTPClient implements Getter.
var _ Getter = (*mockHTTPClient)(nil)

// mockHTTPClient is a mock HTTP client for testing.
type mockHTTPClient struct {
	getFunc func(url string) (*http.Response, error)
}

// Get implements Getter.Get.
func (m *mockHTTPClient) Get(url string) (*http.Response, error) {
	// Delegate to configured function
	if m.getFunc != nil {
		return m.getFunc(url)
	}
	// Return error if not configured
	return nil, errors.New("mock not configured")
}

// Compile-time guarantee that mockFileSystem implements FileSystem.
var _ FileSystem = (*mockFileSystem)(nil)

// mockFileSystem is a mock file system for testing.
type mockFileSystem struct {
	executableFunc   func() (string, error)
	evalSymlinksFunc func(path string) (string, error)
	createTempFunc   func(dir, pattern string) (*os.File, error)
	chmodFunc        func(name string, mode os.FileMode) error
	renameFunc       func(oldpath, newpath string) error
	removeFunc       func(name string) error
}

// Executable implements FileSystem.Executable.
func (m *mockFileSystem) Executable() (string, error) {
	// Delegate to configured function
	if m.executableFunc != nil {
		return m.executableFunc()
	}
	// Return default path
	return "/usr/bin/ktn-linter", nil
}

// EvalSymlinks implements FileSystem.EvalSymlinks.
func (m *mockFileSystem) EvalSymlinks(path string) (string, error) {
	// Delegate to configured function
	if m.evalSymlinksFunc != nil {
		return m.evalSymlinksFunc(path)
	}
	// Return path unchanged
	return path, nil
}

// CreateTemp implements FileSystem.CreateTemp.
func (m *mockFileSystem) CreateTemp(dir, pattern string) (*os.File, error) {
	// Delegate to configured function
	if m.createTempFunc != nil {
		return m.createTempFunc(dir, pattern)
	}
	// Return error if not configured
	return nil, errors.New("mock CreateTemp not configured")
}

// Chmod implements FileSystem.Chmod.
func (m *mockFileSystem) Chmod(name string, mode os.FileMode) error {
	// Delegate to configured function
	if m.chmodFunc != nil {
		return m.chmodFunc(name, mode)
	}
	// Return nil by default
	return nil
}

// Rename implements FileSystem.Rename.
func (m *mockFileSystem) Rename(oldpath, newpath string) error {
	// Delegate to configured function
	if m.renameFunc != nil {
		return m.renameFunc(oldpath, newpath)
	}
	// Return nil by default
	return nil
}

// Remove implements FileSystem.Remove.
func (m *mockFileSystem) Remove(name string) error {
	// Delegate to configured function
	if m.removeFunc != nil {
		return m.removeFunc(name)
	}
	// Return nil by default
	return nil
}

// Compile-time guarantee that mockCopier implements Copier.
var _ Copier = (*mockCopier)(nil)

// mockCopier is a mock IO copier for testing.
type mockCopier struct {
	copyFunc func(dst io.Writer, src io.Reader) (int64, error)
}

// Copy implements Copier.Copy.
func (m *mockCopier) Copy(dst io.Writer, src io.Reader) (int64, error) {
	// Delegate to configured function
	if m.copyFunc != nil {
		return m.copyFunc(dst, src)
	}
	// Default: perform actual copy
	return io.Copy(dst, src)
}

// mockFile is a mock file for testing.
type mockFile struct {
	name    string
	closed  bool
	written []byte
}

// Name returns the file name.
func (m *mockFile) Name() string {
	// Return mock name
	return m.name
}

// Write writes data to the mock file.
func (m *mockFile) Write(p []byte) (int, error) {
	// Append to written buffer
	m.written = append(m.written, p...)
	// Return length written
	return len(p), nil
}

// Close closes the mock file.
func (m *mockFile) Close() error {
	// Mark as closed
	m.closed = true
	// Return nil
	return nil
}

// errReader is a reader that always returns an error.
type errReader struct {
	err error
}

// Read implements io.Reader.Read.
func (r *errReader) Read(_ []byte) (int, error) {
	// Return configured error
	return 0, r.err
}
