package buildinfo_test

import (
	"runtime/debug"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/proc/buildinfo"
	"github.com/kitsunium/sdk/pkg/v1/proc/process"
)

// TestParseBuildThroughTheFacade pins that a consumer can describe a build
// from this package alone: a pseudo-versioned dependency is a commit, not a
// release.
func TestParseBuildThroughTheFacade(t *testing.T) {
	t.Parallel()
	build := buildinfo.ParseBuild(&debug.BuildInfo{
		Main: debug.Module{Path: "example.com/app", Version: "(devel)"},
		Deps: []*debug.Module{{Path: "example.com/dep", Version: "v0.0.0-20260924095948-8cf38860b6ef"}},
	})
	dep, found := build.Module("example.com/dep")
	if !found || dep.Version != "" || dep.Revision != "8cf38860b6ef" || dep.Time.IsZero() {
		t.Errorf("Module(dep) = %+v, %v; want a commit and no release", dep, found)
	}
	if _, ok := buildinfo.Build(); ok {
		if current, _ := debug.ReadBuildInfo(); current == nil {
			t.Error("Build reported information the toolchain did not embed")
		}
	}
}

// TestTheTypesAreProcess pins that the split changed no type: a value read
// here is the one pkg/v1/proc/process names, so a consumer moving its import
// converts nothing (ADR 0172).
func TestTheTypesAreProcess(t *testing.T) {
	t.Parallel()
	var info process.BuildInfo = buildinfo.ParseBuild(&debug.BuildInfo{Main: debug.Module{Path: "example.com/app"}})
	var mod process.Module = info.Main
	if mod.Path != "example.com/app" {
		t.Errorf("Main = %+v", mod)
	}
}
