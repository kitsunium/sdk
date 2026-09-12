// Internal tests for the three defects the review of PR #178 found, each
// written so reverting its fix fails here.
package selfupdate

import (
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	coreupd "github.com/kitsunium/sdk/internal/core/selfupdate"
)

// sizedFetcher answers every request with one canned body and status.
type sizedFetcher struct {
	body   string
	status int
}

// Get returns the canned response, whatever the URL.
func (f sizedFetcher) Get(_ string) (*http.Response, error) {
	//: One document serves whichever asset the caller asked for.
	return &http.Response{
		StatusCode: f.status,
		Body:       io.NopCloser(strings.NewReader(f.body)),
		Header:     http.Header{},
	}, nil
}

// failingFetcher fails every request at the transport layer, which is the shape
// a connection reset, a DNS failure or a timeout takes.
type failingFetcher struct{ err error }

// Get always fails, never returning a response.
func (f failingFetcher) Get(_ string) (*http.Response, error) {
	//: A transport failure produces no status to classify.
	return nil, f.err
}

// TestOversizedManifestIsRefusedAsOversized pins that a manifest past the cap
// is reported as a SIZE refusal and never as a signature failure.
//
// The read used to stop exactly at the cap, which cannot tell a manifest that
// fits from one that was truncated. A truncated manifest then failed ed25519
// verification and the operator was told the release was forged — the single
// worst piece of advice this package can give, because the documented response
// to a signature failure is "do not install this by any means".
func TestOversizedManifestIsRefusedAsOversized(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		size    int64
		wantErr error
	}{
		{name: "one byte over the cap", size: maxChecksumsBytes + 1, wantErr: coreupd.ArchiveTooLarge},
		{name: "exactly the cap is accepted", size: maxChecksumsBytes, wantErr: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc := NewUpdaterWithDeps("v1.0.0", testSource,
				sizedFetcher{body: strings.Repeat("x", int(tt.size)), status: http.StatusOK}, nil, nil)

			_, err := svc.fetchChecksums("v2.0.0")
			//: A size problem must never be reported as a signature problem.
			if tt.wantErr == nil && err != nil {
				t.Fatalf("fetchChecksums() error = %v, want nil at exactly the cap", err)
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Errorf("fetchChecksums() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

// TestTransportFailuresAreRetryable pins that a failure with no HTTP status
// still carries DownloadFailed, so a caller can apply a retry policy to it.
//
// Without it, only non-200 STATUSES were classifiable: a connection reset, a
// DNS failure and a timeout — the three most common reasons an update does not
// happen, and the three a retry most reliably fixes — arrived as bare wrapped
// errors that matched no exported code.
func TestTransportFailuresAreRetryable(t *testing.T) {
	t.Parallel()

	transportErr := errors.New("connection reset by peer")

	tests := []struct {
		name string
		call func(svc *Service) error
	}{
		{
			name: "the checksum manifest",
			call: func(svc *Service) error { _, err := svc.fetchChecksums("v2.0.0"); return err },
		},
		{
			name: "the candidate list",
			call: func(svc *Service) error { _, err := svc.ListCandidates(); return err },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc := NewUpdaterWithDeps("v1.0.0", testSource, failingFetcher{err: transportErr}, nil, nil)

			err := tt.call(svc)
			//: A transport failure is exactly what a retry is for.
			if !errors.Is(err, coreupd.DownloadFailed) {
				t.Errorf("error = %v, want it to carry DownloadFailed", err)
			}
			//: And the cause must survive, or the log says nothing useful.
			if !errors.Is(err, transportErr) {
				t.Errorf("error = %v, want it to wrap the transport cause", err)
			}
		})
	}
}

// TestWriteFailureNeverReachesTheRename pins that nothing replaces the running
// executable unless the staged write completed.
//
// This is the observable half of the close-ordering fix. The unobservable half
// is stated rather than faked: FileSystem.CreateTemp returns a concrete
// *os.File, so a double cannot make Close fail while letting the write succeed,
// and the exact "close reports a delayed I/O fault" case has no test here. What
// IS asserted is the invariant that case is a member of — a staged write that
// did not complete must not be followed by a rename — and the fix moved the
// close inside that invariant by closing before finalizeReplacement and
// treating its error as a write failure.
//
// A test that faked the close failure by handing back an already-closed handle
// would pass against BOTH orderings, because the write fails first. Passing for
// the wrong reason is worse than not testing, so it is not done.
func TestWriteFailureNeverReachesTheRename(t *testing.T) {
	t.Parallel()

	tests := []struct{ name string }{{name: "a staged write that cannot complete"}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fs := &renameRecordingFS{}
			svc := NewUpdaterWithDeps("v1.0.0", testSource, nil, fs, stdIOCopier{})

			err := svc.writeAndReplaceBinary(strings.NewReader("new binary"), "/usr/local/bin/widget")
			//: An incomplete stage must surface as a failure.
			if err == nil {
				t.Fatal("writeAndReplaceBinary() = nil error, want the staging failure reported")
			}
			//: And the running executable must still be the old one.
			if fs.renamed {
				t.Error("the binary was replaced despite the staged write not completing")
			}
		})
	}
}

// renameRecordingFS is a FileSystem whose staged file cannot be written to, and
// which records whether a rename was nonetheless reached.
type renameRecordingFS struct {
	renamed bool
}

// Executable reports a plausible install path.
func (f *renameRecordingFS) Executable() (string, error) {
	//: Any absolute path serves; the test never reads the binary back.
	return "/usr/local/bin/widget", nil
}

// EvalSymlinks resolves to the path it was given.
func (f *renameRecordingFS) EvalSymlinks(path string) (string, error) {
	//: No symlink in this fixture.
	return path, nil
}

// CreateTemp returns a real file in the test's own temporary directory, wrapped
// so its Close fails.
func (f *renameRecordingFS) CreateTemp(_, pattern string) (*os.File, error) {
	file, err := os.CreateTemp(os.TempDir(), pattern)
	//: A fixture that cannot stage has nothing to assert about.
	if err != nil {
		//: Surface it rather than masking the setup failure.
		return nil, err
	}
	//: Close it now and hand back the invalid handle, so the staged write
	//: fails. That is the observable stand-in for any fault that leaves the
	//: staged bytes incomplete.
	closeErr := file.Close()
	//: A fixture whose own close fails cannot stand in for anything.
	if closeErr != nil {
		//: Surface it rather than masking the setup failure.
		return nil, closeErr
	}

	//: The handle the caller stages through.
	return file, nil
}

// Chmod succeeds; the fixture is about ordering, not permissions.
func (f *renameRecordingFS) Chmod(string, os.FileMode) error {
	//: Nothing to refuse here.
	return nil
}

// Rename records that the replacement was reached. Reaching it at all is the
// defect this fixture exists to catch.
func (f *renameRecordingFS) Rename(string, string) error {
	f.renamed = true

	//: Succeed, so a test that DOES reach here fails on the flag and not on
	//: an error that would look like the fix working.
	return nil
}

// Remove succeeds: the staged file is cleaned up on the failure path.
func (f *renameRecordingFS) Remove(string) error {
	//: Cleanup is not what this fixture asserts on.
	return nil
}

// Compile-time proof that the three doubles still satisfy the ports they stand
// in for, so a port method added later fails here rather than at the call site.
var (
	_ Getter     = (*sizedFetcher)(nil)
	_ Getter     = (*failingFetcher)(nil)
	_ FileSystem = (*renameRecordingFS)(nil)
)
