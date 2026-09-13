//go:build windows

package entitlement

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// openWithShareMode opens path for reading with an explicit Windows share
// mode, which os.Open cannot express.
//
// It exists because the share mode is the whole subject: Go's os.Open asks for
// FILE_SHARE_READ|FILE_SHARE_WRITE and never FILE_SHARE_DELETE (go1.27
// syscall/syscall_windows.go), and a destination held without FILE_SHARE_DELETE
// is a destination MoveFileEx may not replace. Measuring the difference needs
// both modes, so the test reaches for CreateFile directly.
func openWithShareMode(t *testing.T, path string, share uint32) syscall.Handle {
	t.Helper()

	namep, convErr := syscall.UTF16PtrFromString(path)
	//: A failure here is an environment problem, not a test outcome.
	if convErr != nil {
		t.Fatalf("encoding %s: %v", path, convErr)
	}
	handle, openErr := syscall.CreateFile(
		namep,
		syscall.GENERIC_READ,
		share,
		nil,
		syscall.OPEN_EXISTING,
		syscall.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	//: A failure here is an environment problem, not a test outcome.
	if openErr != nil {
		t.Fatalf("opening %s with share mode %#x: %v", path, share, openErr)
	}
	//: Released at the end of the test that acquired it; every row below
	//: needs the handle open across the rename it is measuring.
	t.Cleanup(func() {
		//: Best-effort: an unreleased handle would only leak into the
		//: temporary directory's removal, which t.TempDir already tolerates.
		if closeErr := syscall.CloseHandle(handle); closeErr != nil {
			t.Logf("closing handle on %s: %v", path, closeErr)
		}
	})
	//: The caller now holds the destination open under a known share mode.
	return handle
}

// Test_windowsRenameOverAnOpenDestination measures what MoveFileEx actually
// permits, because the review that deferred this defect described a mechanism
// Go does not have.
//
// The finding on PR #180 read: "installs every refreshed roster with
// os.Rename(staged, installed) even when the destination cache file already
// exists, an unsupported replacement operation on Windows". That premise is
// wrong as stated, and the distinction decides the fix. Go's os.Rename on
// Windows is internal/syscall/windows.Rename, which is
// MoveFileEx(from, to, MOVEFILE_REPLACE_EXISTING) — replacement is supported,
// and the first row below is what says so on a real kernel rather than from
// reading source.
//
// What is NOT supported is replacing a destination another handle holds open
// without FILE_SHARE_DELETE, which is exactly how os.Open opens every file on
// Windows. So the defect is real and its cause is a READER, not the rename:
// this package's own readCappedFile, running in a second process, is what makes
// a refresh fail.
//
// The third row is the one this test existed to settle, and windows-latest has
// now settled it. A reader opening with FILE_SHARE_DELETE does NOT let the
// replacement through: MoveFileEx still refuses with ERROR_ACCESS_DENIED,
// because replacing a name is not the same operation as unlinking it and the
// destination's directory entry stays occupied while any handle remains. That
// removes the cheap fix — teaching readCappedFile a share mode — and leaves
// exclusion as the only answer, which is what cache_lock.go implements.
//
// Not parallel: each row holds a handle whose lifetime must not overlap
// another row's rename on a shared path — and each row uses its own directory
// so it does not have to.
func Test_windowsRenameOverAnOpenDestination(t *testing.T) {
	tests := []struct {
		name string
		// share is the destination holder's share mode; 0 means no holder.
		share uint32
		// wantInstalled is whether the rename must succeed.
		wantInstalled bool
		reason        string
	}{
		{
			name:          "no holder",
			share:         0,
			wantInstalled: true,
			reason:        "MOVEFILE_REPLACE_EXISTING is what os.Rename asks for, so replacement itself is supported and the review's premise is wrong",
		},
		{
			name:          "a holder without FILE_SHARE_DELETE, which is what os.Open asks for",
			share:         syscall.FILE_SHARE_READ | syscall.FILE_SHARE_WRITE,
			wantInstalled: false,
			reason:        "this is readCappedFile in a second process, and it is the real cause of the lost refresh",
		},
		{
			name:          "a holder with FILE_SHARE_DELETE",
			share:         syscall.FILE_SHARE_READ | syscall.FILE_SHARE_WRITE | syscall.FILE_SHARE_DELETE,
			wantInstalled: false,
			reason:        "measured on windows-latest: sharing DELETE is not enough, so a share mode on the read path is no fix and exclusion is the only one",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			installed := filepath.Join(dir, "installed")
			staged := filepath.Join(dir, "staged")
			//: A destination that already exists: replacement, not creation,
			//: is what is being measured.
			if err := os.WriteFile(installed, []byte("old"), 0o600); err != nil {
				t.Fatalf("writing the destination: %v", err)
			}
			if err := os.WriteFile(staged, []byte("new"), 0o600); err != nil {
				t.Fatalf("writing the source: %v", err)
			}
			//: Zero means "measure the rename with nobody holding it".
			if tt.share != 0 {
				openWithShareMode(t, installed, tt.share)
			}

			renameErr := os.Rename(staged, installed)
			//: Report the platform's own error either way: the syscall number
			//: is the evidence, and a narrative about Windows is not.
			t.Logf("os.Rename over an existing destination, holder share mode %#x: err = %v", tt.share, renameErr)

			if tt.wantInstalled && renameErr != nil {
				t.Errorf("os.Rename() = %v, want nil (%s)", renameErr, tt.reason)
			}
			if !tt.wantInstalled && renameErr == nil {
				t.Errorf("os.Rename() = nil, want a sharing violation (%s)", tt.reason)
			}
		})
	}
}
