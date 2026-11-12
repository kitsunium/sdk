//go:build !linux

package consensus

import (
	"os"
	"runtime"
)

// rwfDsyncProbe answers that RWF_DSYNC is Linux's: elsewhere the log syncs
// as datasync does (#923).
func rwfDsyncProbe(string) string {
	return "pwritev2(2) with RWF_DSYNC is not available on " + runtime.GOOS
}

// writeDsync writes nothing off Linux: ok is false.
func writeDsync(*os.File, []byte, int64) (ok bool, err error) { return false, nil }
