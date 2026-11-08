//go:build unix || windows

package exec

import (
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// Test_buildStdio_nullIsShared pins #943's cache: every null stream of every
// spawn is the one null device this process opened once, read-write and
// close-on-exec, so a StdioNull spawn after the first opens nothing — and the
// descriptor is in neither cleanup list, since closing it would take it from
// every later spawn.
func Test_buildStdio_nullIsShared(t *testing.T) {
	t.Parallel()
	first, err := buildStdio(coreproc.Spec{Stdio: coreproc.StdioNull})
	if err != nil {
		t.Fatalf("buildStdio = %v", err)
	}
	second, err := buildStdio(coreproc.Spec{Stdio: coreproc.StdioCapture})
	if err != nil {
		t.Fatalf("buildStdio = %v", err)
	}
	null := first.files[fdStdin]
	for _, s := range []*stdioState{first, second} {
		for i, f := range s.files {
			if f != null {
				t.Errorf("fd %d is %v, want the shared null device %v", i, f, null)
			}
		}
		if len(s.opened) != 0 || len(s.closeChild) != 0 {
			t.Errorf("the shared null device is on a cleanup list: opened %v, closeChild %v", s.opened, s.closeChild)
		}
		s.closeAll()
	}
	//: closeAll left it open for the next spawn.
	if _, err := null.Stat(); err != nil {
		t.Errorf("the shared null device was closed: %v", err)
	}
}
