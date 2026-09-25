package process_test

import (
	"os"
	"runtime/debug"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/process"
)

// TestSelfDescribesThisProcess pins the facade over the snapshot: the figures
// are this process's, on every platform, with nothing to handle.
func TestSelfDescribesThisProcess(t *testing.T) {
	t.Parallel()
	stats := process.Self()
	if stats.PID != os.Getpid() || stats.Goroutines < 1 || stats.HeapBytes == 0 {
		t.Errorf("Self() = pid %d, %d goroutines, heap %d; want this process", stats.PID, stats.Goroutines, stats.HeapBytes)
	}
	if stats.Uptime < 0 || stats.Started.After(stats.At) {
		t.Errorf("Self() started %v at %v", stats.Started, stats.At)
	}
}

// TestParseBuildThroughTheFacade pins that a consumer can describe a build
// from the public surface alone: a pseudo-versioned dependency is a commit,
// not a release.
func TestParseBuildThroughTheFacade(t *testing.T) {
	t.Parallel()
	build := process.ParseBuild(&debug.BuildInfo{
		Main: debug.Module{Path: "example.com/app", Version: "(devel)"},
		Deps: []*debug.Module{{Path: "example.com/dep", Version: "v0.0.0-20260924095948-8cf38860b6ef"}},
	})
	dep, found := build.Module("example.com/dep")
	if !found || dep.Version != "" || dep.Revision != "8cf38860b6ef" || dep.Time.IsZero() {
		t.Errorf("Module(dep) = %+v, %v; want a commit and no release", dep, found)
	}
	if _, ok := process.Build(); ok {
		if current, _ := debug.ReadBuildInfo(); current == nil {
			t.Error("Build reported information the toolchain did not embed")
		}
	}
}
