// Package selfupdate — the platform every Service in this suite is built for,
// and the one platform whose replacement step is refused.
package selfupdate

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	coreupd "github.com/kitsunium/sdk/internal/core/selfupdate"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// fixtureGOOS is the platform every Service this suite constructs is built
// for; see TestMain.
const fixtureGOOS string = "linux"

// TestMain pins the platform every Service in this suite is built for.
//
// The update pipeline is platform-independent code steered by two platform
// facts — the archive format and the asset name — and its fixtures are built
// for ONE platform: a tar.gz, and a manifest naming that platform's asset. Left
// to runtime.GOOS they changed meaning with the host: on Windows every Service
// expected a zip, met the tar.gz and reported ARCHIVE_UNREADABLE; and since
// canReplace a Windows Service refuses before the download, so each gate case
// would have tested that refusal instead of its gate. Pinning runtimeGOOS ONCE,
// before any test runs, gives every host the same verdict and races nothing —
// the Service comment's warning is about mutating it under t.Parallel.
//
// Nothing about Windows goes untested for it: the Windows answer is asserted
// on Services built for Windows explicitly, on every host, below — and the
// zip and .exe shapes by the tables that already build a Service per platform.
func TestMain(m *testing.M) {
	runtimeGOOS = func() string { return fixtureGOOS }
	os.Exit(m.Run())
}

// recordingGetter answers every URL with an error or a canned body, and keeps
// the URLs it was asked for so a test can prove what was — and was not —
// fetched.
type recordingGetter struct {
	mu     sync.Mutex
	asked  []string
	answer func(url string) (*http.Response, error)
}

// Get implements Getter, recording the URL first.
func (r *recordingGetter) Get(url string) (*http.Response, error) {
	r.record(url)
	//: a getter with no answer is offline.
	if r.answer == nil {
		return nil, errors.New("recordingGetter: offline")
	}
	return r.answer(url)
}

// record keeps one URL the getter was asked for.
func (r *recordingGetter) record(url string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.asked = append(r.asked, url)
}

// urls returns the URLs asked for so far.
func (r *recordingGetter) urls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.asked)
}

// untouchedFS is a FileSystem that fails the test the moment anything tries
// to stage, chmod, rename or remove: a refused replacement must write nothing.
func untouchedFS(t *testing.T) *mockFileSystem {
	t.Helper()
	return &mockFileSystem{
		createTempFunc: func(dir, _ string) (*os.File, error) {
			t.Errorf("CreateTemp(%q) was called — a refused replacement staged a file", dir)
			return nil, errors.New("refused")
		},
		chmodFunc: func(name string, _ os.FileMode) error {
			t.Errorf("Chmod(%q) was called — a refused replacement touched the disk", name)
			return nil
		},
		renameFunc: func(oldpath, newpath string) error {
			t.Errorf("Rename(%q, %q) was called — a refused replacement replaced something", oldpath, newpath)
			return nil
		},
		removeFunc: func(name string) error {
			t.Errorf("Remove(%q) was called — a refused replacement touched the disk", name)
			return nil
		},
	}
}

// TestAWindowsTargetRefusesTheReplacementBeforeDownloadingAnything pins the
// Windows contract ADR 0077 documented and nothing enforced.
//
// Windows will not let a running executable be renamed over, so the step
// cannot complete; the old flow found that out after downloading and
// authenticating the archive, fell back to `sudo -n mv` — a command Windows
// does not have — and ended advising the sudo opt-in. Now a Service built for
// Windows refuses with UNSUPPORTED_PLATFORM before a byte of the release is
// fetched, and a build with no vendor key is still told about the key first,
// on every platform. The last row is the control: the same Service built for
// Linux goes on to the download.
//
// It runs on every host, because what it pins is a property of the Service's
// target platform, not of the machine running the suite.
func TestAWindowsTargetRefusesTheReplacementBeforeDownloadingAnything(t *testing.T) {
	t.Parallel()
	pub, _ := vendorKeypair(t)
	type tc struct {
		name string
		// goos is the platform the Service is built for.
		goos string
		// keyed gives the Service a vendor key.
		keyed bool
		// want is the refusal expected, and whether any URL may be fetched.
		want        errs.Code
		wantFetches bool
	}
	tests := []tc{
		{name: "a windows target, before any download", goos: "windows", keyed: true, want: coreproc.CodeUnsupportedPlatform},
		{name: "a windows target with no vendor key hears about the key first", goos: "windows", want: coreupd.CodeNoVendorKey},
		{name: "a linux target goes on to the download", goos: "linux", keyed: true, want: coreupd.CodeDownloadFailed, wantFetches: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		getter := &recordingGetter{}
		svc := NewUpdaterWithDeps("v1.0.0", testSource, getter, untouchedFS(t), &mockCopier{})
		svc.goos = c.goos
		if c.keyed {
			svc = svc.WithVendorKey(pub)
		}

		err := svc.downloadAndReplace("v2.0.0")

		if !errs.HasCode(err, c.want) {
			t.Fatalf("downloadAndReplace on a %s target = %v, want code %v", c.goos, err, c.want)
		}
		if fetched := getter.urls(); (len(fetched) > 0) != c.wantFetches {
			t.Fatalf("fetched %v, want fetches=%v", fetched, c.wantFetches)
		}
		//: the public side matches the sentinel itself, not only its code.
		if c.want == coreproc.CodeUnsupportedPlatform && !errors.Is(err, coreproc.UnsupportedPlatform) {
			t.Errorf("errors.Is(%v, UnsupportedPlatform) = false, want true", err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestUpgradeOnAWindowsTargetChecksAndThenStops pins the same refusal through
// the call a product actually makes. Upgrade still asks whether there IS a
// newer release — CheckForUpdate works on Windows and is worth answering —
// and then stops: the release metadata is the only thing fetched.
func TestUpgradeOnAWindowsTargetChecksAndThenStops(t *testing.T) {
	t.Parallel()
	pub, _ := vendorKeypair(t)
	getter := &recordingGetter{answer: func(url string) (*http.Response, error) {
		//: only the release metadata has an answer; anything else fails loudly.
		if !strings.Contains(url, "/releases/latest") {
			return nil, errors.New("recordingGetter: " + url + " should never have been fetched")
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{},
			Body:       io.NopCloser(bytes.NewReader([]byte(`{"tag_name":"v2.0.0","draft":false,"prerelease":false}`))),
		}, nil
	}}
	svc := NewUpdaterWithDeps("v1.0.0", testSource, getter, untouchedFS(t), &mockCopier{}).WithVendorKey(pub)
	svc.goos = "windows"

	info, err := svc.Upgrade()

	if !errs.HasCode(err, coreproc.CodeUnsupportedPlatform) {
		t.Fatalf("Upgrade on a windows target = %v, want UNSUPPORTED_PLATFORM", err)
	}
	if !info.Available || info.LatestVersion != "v2.0.0" {
		t.Errorf("Upgrade reported %+v, want the newer release it found", info)
	}
	if fetched := getter.urls(); len(fetched) != 1 {
		t.Errorf("fetched %v, want the release metadata alone", fetched)
	}
}
