package sqlite_test

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"testing"

	"github.com/kitsunium/sdk/framework/connectors/sqlite"
	"github.com/kitsunium/sdk/pkg/v1/proc"
	"github.com/kitsunium/sdk/pkg/v1/security/secret"
)

// TestMain runs the suite where modernc.org/sqlite has a port, and where it
// has none — DragonFly, illumos and Solaris, driver_none.go — asserts the
// connector's one answer there, the platform refusal, instead: every case of
// this package opens a SQLite file, a fixture that cannot exist on those
// kernels. The list is spelled out rather than probed, so that a refusal
// anywhere else fails the suite instead of passing it.
func TestMain(m *testing.M) {
	switch runtime.GOOS {
	case "dragonfly", "illumos", "solaris":
		db, err := sqlite.Engine().Open(secret.FromString("file:refused.sqlite"), nil)
		if !errors.Is(err, proc.UnsupportedPlatform) || db != nil {
			fmt.Fprintf(os.Stderr, "sqlite.Engine().Open on %s = (%v, %v), want (nil, UNSUPPORTED_PLATFORM)\n", runtime.GOOS, db, err)
			os.Exit(1)
		}
		fmt.Printf("modernc.org/sqlite has no port to %s: the connector's refusal is asserted, and no case of this package can open a file there\n", runtime.GOOS)
		os.Exit(0)
	}
	os.Exit(m.Run())
}
