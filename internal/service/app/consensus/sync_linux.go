//go:build linux

package consensus

import (
	"os"
	"strings"
	"syscall"
)

// datasync makes a segment's written bytes durable with fdatasync(2): the
// segment is zero-filled at its full size, so there is no size to publish
// (D4, R1-09).
func datasync(f *os.File) error {
	for {
		err := syscall.Fdatasync(int(f.Fd()))
		if err != syscall.EINTR {
			return err
		}
	}
}

// bootID is the kernel's boot identity: a STORAGE_FAILED marker carrying it
// refuses a restart in the same boot (R8-05).
func bootID() string {
	b, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// runtimeMarkerDir is the boot-scoped tmpfs path of the second marker.
func runtimeMarkerDir() string { return "/run/kitsunium-consensus" }
