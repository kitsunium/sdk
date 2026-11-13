//go:build !linux

package consensus

import (
	"os"
	"runtime"
)

// fallocateProbe answers that fallocate(2) is Linux's: elsewhere the log
// zero-fills its segments (#922); a Windows backend stays deferred (D4).
func fallocateProbe(string) string { return "fallocate(2) is not available on " + runtime.GOOS }

// fallocateRange reserves nothing off Linux: ok is false.
func fallocateRange(*os.File, int64, int64) (ok bool, err error) { return false, nil }
