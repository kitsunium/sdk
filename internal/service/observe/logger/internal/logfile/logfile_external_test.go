// Package logfile_test — the hardened open from the outside: what it opens,
// what it refuses, and that every refusal carries the CALLING sink's code and
// the same two fields, whichever sink it is and whichever check caught it.
package logfile_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/observe/logger/internal/logfile"
)

// filePerm is the mode both sinks pass Open.
const filePerm os.FileMode = 0o600

// specFor builds a stand-in sink's refusals under one test code, so two sinks'
// refusals are told apart by their codes alone.
func specFor(pkg errs.PkgCode) *logfile.RefusalSpec {
	code := errs.Pack(0, 3, pkg, 1)
	return &logfile.RefusalSpec{
		Symlink: errs.WrapParams{
			Code: code, Reason: "TEST_OPEN_FAILED",
			Public: "The test sink refuses a symlink", Private: "logfile_test: policy refusal",
		},
		Open: errs.WrapParams{
			Code: code, Reason: "TEST_OPEN_FAILED",
			Public: "The test sink could not open its file", Private: "logfile_test: open failure",
		},
	}
}

// field returns the rendered value of key on err, and whether it is there.
func field(err error, key string) (value string, found bool) {
	var typed *errs.Error
	if !errors.As(err, &typed) {
		return "", false
	}
	for _, f := range typed.Fields() {
		if f.Key() == key {
			return f.StringValue(), true
		}
	}
	return "", false
}

// TestOpenAppends pins the happy path: the file is created, written at its
// end, and a second Open appends after what the first wrote.
func TestOpenAppends(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "app.log")
	for _, line := range []string{"first\n", "second\n"} {
		f, err := logfile.Open(path, filePerm, specFor(0xF1))
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		if _, werr := f.WriteString(line); werr != nil {
			t.Fatalf("write: %v", werr)
		}
		if cerr := f.Close(); cerr != nil {
			t.Fatalf("close: %v", cerr)
		}
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "first\nsecond\n" {
		t.Errorf("the file holds %q (%v), want both lines in order", got, err)
	}
}

// TestARefusalCarriesTheCallersCodeAndBothFields pins the seam and the
// legibility together: a planted link is refused under the code of the sink
// that asked — two sinks, two codes — with path and kind=symlink, and its
// target is left exactly as it was; an ordinary failure carries the path and
// NO kind, so a full disk never reads as an attack.
func TestARefusalCarriesTheCallersCodeAndBothFields(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, "victim.txt")
	if werr := os.WriteFile(target, []byte("DATA"), filePerm); werr != nil {
		t.Fatalf("seed target: %v", werr)
	}
	link := filepath.Join(dir, "app.log")
	//: an unplantable link leaves the refusal unverified, so fail, not skip.
	if lerr := os.Symlink(target, link); lerr != nil {
		t.Fatalf("plant symlink: %v", lerr)
	}
	missing := filepath.Join(dir, "no-such-dir", "app.log")
	for _, pkg := range []errs.PkgCode{0xF1, 0xF2} {
		code := errs.Pack(0, 3, pkg, 1)
		f, err := logfile.Open(link, filePerm, specFor(pkg))
		if f != nil || !errs.HasCode(err, code) {
			t.Fatalf("Open(link) = %v, %v; want the caller's %s", f, err, code)
		}
		if kind, found := field(err, "kind"); !found || kind != logfile.KindSymlink {
			t.Errorf("the link refusal's kind = %q (%v), want %q", kind, found, logfile.KindSymlink)
		}
		if path, found := field(err, "path"); !found || path != link {
			t.Errorf("the link refusal's path = %q (%v), want %q", path, found, link)
		}
		f, err = logfile.Open(missing, filePerm, specFor(pkg))
		if f != nil || !errs.HasCode(err, code) || !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("Open(missing dir) = %v, %v; want the caller's %s over ErrNotExist", f, err, code)
		}
		if kind, found := field(err, "kind"); found {
			t.Errorf("an ordinary failure carries kind=%q; it must carry none", kind)
		}
	}
	if got, err := os.ReadFile(target); err != nil || string(got) != "DATA" {
		t.Errorf("the link's target changed under a refused open: %q (%v)", got, err)
	}
}
