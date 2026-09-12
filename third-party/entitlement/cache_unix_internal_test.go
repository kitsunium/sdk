//go:build !windows

package entitlement

import (
	"errors"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// Test_DefaultCacheDir_unixDisablesItselfWithoutAHome pins that a machine
// whose cache root cannot be named gets NO fallback rather than a relative
// path.
//
// It is unix-only and build-tagged rather than skipped: os.UserCacheDir reads
// XDG_CACHE_HOME then HOME here and LocalAppData on Windows, so the variables
// that make it fail are not the same ones — KTN-TEST-NOSKIP forbids hiding
// that behind a runtime skip, and the Windows case has no equivalent to assert
// without guessing at its environment.
//
// Returning "" disables the offline fallback, which is the safe direction: a
// machine that cannot say where its cache lives requires the network, exactly
// as this package did before a cache existed. The failure this rules out is a
// RELATIVE path, which would drop a roster wherever the linter was invoked.
//
// Not parallel: t.Setenv.
func Test_DefaultCacheDir_unixDisablesItselfWithoutAHome(t *testing.T) {
	tests := []struct {
		name   string
		want   string
		reason string
	}{
		{name: "no XDG_CACHE_HOME and no HOME", want: "", reason: "disabled beats invented"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			//: Both, in this order: UserCacheDir prefers XDG_CACHE_HOME and
			//: falls back to $HOME/.cache, so clearing one alone proves
			//: nothing.
			t.Setenv("XDG_CACHE_HOME", "")
			t.Setenv("HOME", "")

			if got := testProduct.DefaultCacheDir(); got != tt.want {
				t.Errorf("testProduct.DefaultCacheDir() = %q, want %q (%s)", got, tt.want, tt.reason)
			}
		})
	}
}

// Test_readCappedFile_unixRefusesAFifoWithoutBlocking pins that a named pipe in
// the cache directory cannot hang the licence gate.
//
// Opening a FIFO for reading blocks until somebody opens the write end, and
// nobody ever will. This read happens on the clock ratchet, BEFORE any origin
// is contacted, so without the regular-file guard a pipe dropped into
// ~/.cache/ktn-linter would hang every gated command on a machine whose network
// is perfectly healthy — with no timeout anywhere to end it.
//
// The call runs in a goroutine against a deadline rather than inline: a test
// for a hang that hangs reports a suite-wide timeout, which says far less than
// a named failure on the one function at fault.
//
// Unix-only and build-tagged: syscall.Mkfifo does not exist on Windows, and
// Windows named pipes are a different object reached a different way. There is
// nothing to assert there rather than a skip to write.
func Test_readCappedFile_unixRefusesAFifoWithoutBlocking(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		reason string
	}{
		{name: "a named pipe is refused before it is opened", reason: "the ratchet reads this before any network timeout exists to save it"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(t.TempDir(), cachedBundleName)
			//: A real FIFO with no writer: opening it read-only blocks.
			if err := syscall.Mkfifo(path, 0o600); err != nil {
				t.Fatalf("creating fifo: %v", err)
			}

			//: Buffered, so the goroutine cannot leak if this test fails.
			done := make(chan error, 1)
			//: Goroutine lifecycle: performs one read and sends its result;
			//: exits immediately after, or stays blocked forever on a
			//: regression — which is precisely what the deadline below
			//: reports.
			go func() { _, readErr := readCappedFile(path); done <- readErr }()

			select {
			case err := <-done:
				if !errors.Is(err, ErrRosterUnreachable) {
					t.Errorf("readCappedFile() error = %v, want ErrRosterUnreachable (%s)", err, tt.reason)
				}
			case <-time.After(2 * time.Second):
				t.Errorf("readCappedFile() blocked on a fifo — every gated command would hang here (%s)", tt.reason)
			}
		})
	}
}
