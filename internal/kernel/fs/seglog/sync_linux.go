//go:build linux

package seglog

import (
	"os"
	"syscall"
)

// datasync makes a segment's written bytes durable with fdatasync(2): the
// segment is zero-filled at its full size, so there is no size to publish.
func datasync(f *os.File) error {
	for {
		err := syscall.Fdatasync(int(f.Fd()))
		if err != syscall.EINTR {
			return err
		}
	}
}
