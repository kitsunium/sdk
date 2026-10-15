// White-box tests for WithCompanions: a set of files replaced from one
// verified archive, all or none.
package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	coreupd "github.com/kitsunium/sdk/framework/internal/core/selfupdate"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// companionName is the companion the suite declares: a name the
// implementation never mentions, like testSource's product.
const companionName string = "widget-hook"

// setFS is the real file system, Link included, anchored at a test binary,
// with one rename that can be made to fail.
type setFS struct {
	osFileSystem
	exe        string
	failRename string
}

// Executable names the test binary rather than the test process.
func (f setFS) Executable() (string, error) {
	return f.exe, nil
}

// Rename fails for the one target the scenario names, and renames otherwise.
func (f setFS) Rename(oldpath, newpath string) error {
	//: The scenario's injected failure.
	if newpath == f.failRename {
		return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: os.ErrInvalid}
	}
	return os.Rename(oldpath, newpath)
}

// makeTarGzMembers returns a tar.gz holding one regular file per name/body pair.
func makeTarGzMembers(t *testing.T, members ...[2]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, m := range members {
		hdr := &tar.Header{Name: m[0], Mode: 0o755, Size: int64(len(m[1])), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("tar header: %v", err)
		}
		if _, err := tw.Write([]byte(m[1])); err != nil {
			t.Fatalf("tar write: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

// setFixture signs a release around a given archive.
func setFixture(t *testing.T, archive []byte) releaseFixture {
	t.Helper()
	pub, priv := vendorKeypair(t)
	manifest := checksumsManifestFor(t, archive)
	return releaseFixture{pub: pub, priv: priv, archive: archive, manifest: manifest, sig: signManifest(priv, manifest)}
}

// installSet writes the old binary, and the old companion when asked, into a
// fresh directory and returns the two paths.
func installSet(t *testing.T, withCompanion bool) (exe, companion string) {
	t.Helper()
	dir := t.TempDir()
	exe = filepath.Join(dir, testSource.Product)
	companion = filepath.Join(dir, companionName)
	if err := os.WriteFile(exe, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if withCompanion {
		if err := os.WriteFile(companion, []byte("old companion"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return exe, companion
}

// readOr returns a file's content, or "<absent>" when it does not exist.
func readOr(t *testing.T, path string) string {
	t.Helper()
	got, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "<absent>"
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(got)
}

// serveFixture answers the release assets of fixture, and 404 otherwise.
func serveFixture(t *testing.T, fixture *releaseFixture) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !fixture.serveAsset(t, w, r.URL.Path) {
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

// The whole contract, one row per promise: both files from one verified
// archive, a companion not installed never created, an unverified archive
// replacing nothing, a failed second rename putting the first back, a member
// the archive lacks and a file system that cannot link replacing nothing.
func TestWithCompanionsReplacesTheSetFromOneVerifiedArchive(t *testing.T) {
	t.Parallel()
	both := [][2]string{{testSource.Product, "new binary"}, {companionName, "new companion"}}

	tests := []struct {
		name          string
		members       [][2]string
		withCompanion bool
		mutate        func(t *testing.T, f *releaseFixture)
		failSecond    bool
		noLink        bool
		wantErrIs     error
		wantFields    string
		wantExe       string
		wantCompanion string
		wantEntries   []string
	}{
		{
			name: "an installed companion is replaced with the binary", members: both, withCompanion: true,
			wantExe: "new binary", wantCompanion: "new companion",
			wantEntries: []string{testSource.Product, companionName},
		},
		{
			name: "a companion not installed is not created", members: both,
			wantExe: "new binary", wantCompanion: "<absent>",
			wantEntries: []string{testSource.Product},
		},
		{
			name: "an archive the vendor did not sign replaces neither", members: both, withCompanion: true,
			mutate: func(t *testing.T, f *releaseFixture) {
				t.Helper()
				_, impostor := vendorKeypair(t)
				f.sig = signManifest(impostor, f.manifest)
			},
			wantErrIs: coreupd.SignatureInvalid, wantExe: "old binary", wantCompanion: "old companion",
			wantEntries: []string{testSource.Product, companionName},
		},
		{
			name: "an archive the signed manifest does not cover replaces neither", members: both, withCompanion: true,
			mutate: func(t *testing.T, f *releaseFixture) {
				t.Helper()
				f.manifest = []byte(strings.Repeat("a", sha256HexLen) + "  " + hostAssetName() + "\n")
				f.sig = signManifest(f.priv, f.manifest)
			},
			wantErrIs: coreupd.ChecksumMismatch, wantExe: "old binary", wantCompanion: "old companion",
			wantEntries: []string{testSource.Product, companionName},
		},
		{
			name: "a second rename that fails puts the binary back", members: both, withCompanion: true,
			failSecond: true, wantErrIs: ReplacementFailed, wantFields: "rolled_back=true",
			wantExe: "old binary", wantCompanion: "old companion",
			wantEntries: []string{testSource.Product, companionName},
		},
		{
			name: "a companion the archive lacks replaces nothing", withCompanion: true,
			members:   [][2]string{{testSource.Product, "new binary"}},
			wantErrIs: coreupd.BinaryNotInArchive, wantExe: "old binary", wantCompanion: "old companion",
			wantEntries: []string{testSource.Product, companionName},
		},
		{
			name: "a file system that cannot link replaces nothing", members: both, withCompanion: true,
			noLink: true, wantErrIs: StagingFailed, wantFields: "linkable=false",
			wantExe: "old binary", wantCompanion: "old companion",
			wantEntries: []string{testSource.Product, companionName},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			exe, companion := installSet(t, tc.withCompanion)
			fixture := setFixture(t, makeTarGzMembers(t, tc.members...))
			if tc.mutate != nil {
				tc.mutate(t, &fixture)
			}
			var fsys FileSystem = setFS{exe: exe}
			if tc.failSecond {
				fsys = setFS{exe: exe, failRename: companion}
			}
			if tc.noLink {
				real := newRealBackedFS(exe)
				real.evalSymlinksFunc = filepath.EvalSymlinks
				fsys = real
			}
			svc := signedServiceFor(t, fixture.pub, fsys, serveFixture(t, &fixture)).WithCompanions(companionName)

			err := svc.downloadAndReplace("v2.0.0")

			if tc.wantErrIs == nil && err != nil {
				t.Fatalf("downloadAndReplace() = %v (%s)", err, diagnose(err))
			}
			if tc.wantErrIs != nil && !errors.Is(err, tc.wantErrIs) {
				t.Fatalf("downloadAndReplace() = %v, want errors.Is %v", err, tc.wantErrIs)
			}
			if tc.wantFields != "" && !strings.Contains(fieldsOf(err), tc.wantFields) {
				t.Errorf("fields = %s, want %s", fieldsOf(err), tc.wantFields)
			}
			if got := readOr(t, exe); got != tc.wantExe {
				t.Errorf("binary = %q, want %q", got, tc.wantExe)
			}
			if got := readOr(t, companion); got != tc.wantCompanion {
				t.Errorf("companion = %q, want %q", got, tc.wantCompanion)
			}
			//: No staged file and no kept link is left behind, whatever happened.
			if got := dirEntries(t, filepath.Dir(exe)); !slices.Equal(got, tc.wantEntries) {
				t.Errorf("directory = %v, want %v", got, tc.wantEntries)
			}
		})
	}
}

// A companion reached through a symlink is replaced at its real path, and the
// link is left a link — the rule the binary itself follows.
func TestWithCompanionsReplacesASymlinkedCompanionAtItsRealPath(t *testing.T) {
	t.Parallel()
	exe, companion := installSet(t, false)
	elsewhere := filepath.Join(t.TempDir(), companionName)
	if err := os.WriteFile(elsewhere, []byte("old companion"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, companion); err != nil {
		t.Fatal(err)
	}
	fixture := setFixture(t, makeTarGzMembers(t, [2]string{testSource.Product, "new binary"}, [2]string{companionName, "new companion"}))
	svc := signedServiceFor(t, fixture.pub, setFS{exe: exe}, serveFixture(t, &fixture)).WithCompanions(companionName)

	if err := svc.downloadAndReplace("v2.0.0"); err != nil {
		t.Fatalf("downloadAndReplace() = %v (%s)", err, diagnose(err))
	}
	if got := readOr(t, elsewhere); got != "new companion" {
		t.Errorf("real companion = %q, want the new one", got)
	}
	if info, err := os.Lstat(companion); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the link was not kept a link: %v %v", info, err)
	}
}

// A probe that fails puts back the binary AND its companions; one that passes
// keeps the previous files as <file>.prev, as the single-binary probe does.
func TestWithCompanionsAFailedProbePutsTheWholeSetBack(t *testing.T) {
	t.Parallel()
	for _, pass := range []bool{true, false} {
		exe, companion := installSet(t, true)
		fixture := setFixture(t, makeTarGzMembers(t, [2]string{testSource.Product, "new binary"}, [2]string{companionName, "new companion"}))
		svc := signedServiceFor(t, fixture.pub, setFS{exe: exe}, serveFixture(t, &fixture)).
			WithCompanions(companionName).WithProbe([]string{"version"}, time.Second)
		svc.probe.run = func(context.Context, string, []string) error {
			if pass {
				return nil
			}
			return errs.NewRuntime(0x00_03_42_FF, "PROBE_TEST", "the new binary crashed", "test")
		}

		err := svc.downloadAndReplace("v2.0.0")

		gotExe, gotCompanion := readOr(t, exe), readOr(t, companion)
		switch {
		case pass && (err != nil || gotExe != "new binary" || gotCompanion != "new companion" ||
			readOr(t, exe+prevSuffix) != "old binary" || readOr(t, companion+prevSuffix) != "old companion"):
			t.Errorf("a passing probe: err=%v binary=%q companion=%q", err, gotExe, gotCompanion)
		case !pass && (!errs.HasCode(err, CodeProbeFailed) || gotExe != "old binary" || gotCompanion != "old companion" ||
			!strings.Contains(fieldsOf(err), "rolled_back=true")):
			t.Errorf("a failing probe: err=%v (%s) binary=%q companion=%q", err, fieldsOf(err), gotExe, gotCompanion)
		}
	}
}

// The option chains like every other, keeps its own copy of the names, and a
// nil Service stays nil.
func TestWithCompanionsChainsAndCopies(t *testing.T) {
	t.Parallel()
	var none *Service
	if none.WithCompanions(companionName) != nil {
		t.Error("a nil Service did not stay nil")
	}
	names := []string{companionName}
	svc := NewService("v1.0.0", testSource)
	if svc.WithCompanions(names...) != svc {
		t.Error("WithCompanions did not return its Service")
	}
	names[0] = "mutated"
	if !slices.Equal(svc.companions, []string{companionName}) {
		t.Errorf("companions = %v, want a copy of the caller's names", svc.companions)
	}
}
