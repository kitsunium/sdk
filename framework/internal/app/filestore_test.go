package app_test

import (
	"errors"
	"runtime"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/data/vfs"
	"github.com/kitsunium/sdk/pkg/v1/proc"
)

// needsFileStore skips t where the SDK's file store refuses to exist, after
// asserting that refusal: vfs.NewOS is Linux's, macOS's and the BSDs' (ADR
// 0018, ADR 0056) — on Windows and Plan 9 a file mode is not an access list,
// illumos and Solaris have neither a flushable directory handle nor a file
// lock (ADR 0144) —, so an app with a data directory cannot start there. The
// set is spelled out rather than probed, so that a refusal on Linux, macOS or
// a BSD fails the case instead of skipping it.
func needsFileStore(t *testing.T) {
	t.Helper()
	if !fileStoreRefused() {
		return
	}
	fsys, err := vfs.NewOS(t.TempDir())
	if !errors.Is(err, proc.UnsupportedPlatform) || fsys != nil {
		t.Fatalf("vfs.NewOS on %s = (%v, %v), want (nil, UNSUPPORTED_PLATFORM)", runtime.GOOS, fsys, err)
	}
	t.Skipf("the SDK's file store refuses %s by design (ADR 0018, ADR 0056, ADR 0144); the refusal is asserted above", runtime.GOOS)
}

// fileStoreRefused reports whether the SDK's file stores — the disk file
// system and the secrets' file store — refuse this GOOS by design: every GOOS
// but Linux, macOS and the BSDs.
func fileStoreRefused() bool {
	switch runtime.GOOS {
	case "linux", "android", "darwin", "ios", "freebsd", "openbsd", "netbsd", "dragonfly":
		return false
	}
	return true
}
