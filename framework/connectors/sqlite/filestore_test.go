package sqlite_test

import (
	"runtime"
	"testing"
)

// needsFileStore skips t where the SDK's file store refuses to exist: on
// Windows and Plan 9 a file mode is not an access list, and vfs.NewOS
// refuses by design (ADR 0018), so an app with a data directory cannot start.
func needsFileStore(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" || runtime.GOOS == "plan9" {
		t.Skip("the SDK's file store needs file modes that are access lists")
	}
}
