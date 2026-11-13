//go:build linux

package consensus

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// fallocZeroRange is FALLOC_FL_ZERO_RANGE of linux/falloc.h, which the
// syscall package does not name.
const fallocZeroRange uint32 = 0x10

// fallocateProbeName is the file an open's probe reserves and removes; a
// crash leaves a .tmp, which recovery deletes as it deletes every one.
const fallocateProbeName = "fallocate-probe.tmp"

// fallocateProbe reserves 4 KiB of a fresh file in dir with fallocate(2)
// and removes it: the reason the filesystem cannot, empty when it can.
func fallocateProbe(dir string) string {
	path := filepath.Join(dir, fallocateProbeName)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return "probe: " + err.Error()
	}
	// Closed first, then removed: the probe file is nothing to keep.
	defer func() {
		abandon(f.Close())
		abandon(os.Remove(path))
	}()
	ok, err := fallocateRange(f, 0, 4096)
	switch {
	case err != nil:
		return "probe: " + err.Error()
	case !ok:
		return "the filesystem answers EOPNOTSUPP"
	}
	return ""
}

// fallocateRange makes f's range from off to size read as zeros with
// fallocate(2), its blocks reserved and the file at least size long: mode 0
// over the range, after FALLOC_FL_ZERO_RANGE over whatever bytes the file
// already holds in it (a segment's .tmp is created empty, so it has none).
// ok is false when the filesystem answers EOPNOTSUPP to mode 0; bytes it
// holds that ZERO_RANGE cannot zero are written over with zeros.
func fallocateRange(f *os.File, off, size int64) (ok bool, err error) {
	if off >= size {
		return true, nil
	}
	st, err := f.Stat()
	if err != nil {
		return false, err
	}
	if end := min(st.Size(), size); end > off {
		zok, err := fallocate(f, fallocZeroRange, off, end-off)
		if err != nil {
			return false, err
		}
		if !zok {
			if err := zeroRange(f, off, end); err != nil {
				return false, err
			}
		}
	}
	return fallocate(f, 0, off, size-off)
}

// fallocate is fallocate(2), retried on EINTR; ok is false when the
// filesystem does not support mode.
func fallocate(f *os.File, mode uint32, off, n int64) (ok bool, err error) {
	for {
		err = syscall.Fallocate(int(f.Fd()), mode, off, n)
		if !errors.Is(err, syscall.EINTR) {
			break
		}
	}
	if errors.Is(err, syscall.EOPNOTSUPP) || errors.Is(err, syscall.ENOSYS) {
		return false, nil
	}
	return err == nil, err
}
