package transform_test

import (
	"os"
	"runtime/debug"
	"testing"
)

// testHeapLimit is the soft limit TestMain sets on this test binary's heap.
const testHeapLimit int64 = 640 << 20

// TestMain bounds this binary's heap before any test runs. The registered
// compressors (TestRegisteredSingletonsAreBounded) keep one zstd encoder per
// P alive for the whole run, and the suite builds and drops many more: every
// level, every dictionary, the fuzz targets' own. Under GOGC's default the
// collector lets that garbage grow the heap to about twice the live set
// before it runs — 1.1 GiB resident, measured at GOMAXPROCS=4 — which the
// OpenBSD guest of the cross-platform job refused mid-run ("fatal error:
// runtime: out of memory", in FuzzRoundTripIsExact). With a soft limit the
// collector runs before the heap reaches it: the whole run then peaks at
// 645 MiB at GOMAXPROCS=4 and 641 MiB at 8, in the same time. A GOMEMLIMIT
// the caller sets is left alone.
func TestMain(m *testing.M) {
	//: the caller's own limit wins.
	if _, set := os.LookupEnv("GOMEMLIMIT"); !set {
		debug.SetMemoryLimit(testHeapLimit)
	}
	os.Exit(m.Run())
}
