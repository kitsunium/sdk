package self_test

import (
	"runtime/debug"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/service/proc/self"
)

// Module paths the fixtures describe.
const (
	appPath      string = "github.com/acme/app"
	frameworkMod string = "github.com/acme/framework"
	sdkMod       string = "github.com/kitsunium/sdk/pkg"
)

// TestParseBuild pins how each shape the toolchain records reads: a release,
// a pseudo-version (a commit, not a release), a directory replacement, a
// module replacement, a workspace module, and a main module's VCS stamp with
// and without the "+dirty" Go 1.24 appends.
func TestParseBuild(t *testing.T) {
	t.Parallel()
	stampTime := time.Date(2026, 9, 24, 9, 59, 48, 0, time.UTC)
	info := &debug.BuildInfo{
		GoVersion: "go1.27.1",
		Path:      appPath + "/cmd/app",
		Main:      debug.Module{Path: appPath, Version: "v0.0.0-20260924095948-8cf38860b6ef+dirty"},
		Deps: []*debug.Module{
			{Path: frameworkMod, Version: "v0.0.0", Replace: &debug.Module{Path: "/src/framework", Version: "(devel)"}},
			{Path: sdkMod, Version: "v0.4.6"},
			{Path: "example.com/pseudo", Version: "v1.2.4-0.20260924100234-23e4c32e7484"},
			{Path: "example.com/forked", Version: "v1.0.0", Replace: &debug.Module{Path: "example.com/fork", Version: "v1.0.1"}},
			{Path: "example.com/workspace", Version: "(devel)"},
			nil,
		},
		Settings: []debug.BuildSetting{
			{Key: "GOOS", Value: "linux"},
			{Key: "vcs", Value: "git"},
			{Key: "vcs.revision", Value: "8cf38860b6efa4146621893cc88085d66794a25c"},
			{Key: "vcs.time", Value: "2026-09-24T09:59:48Z"},
			{Key: "vcs.modified", Value: "false"},
		},
	}
	build := self.ParseBuild(info)
	if build.GoVersion != "go1.27.1" || build.Path != appPath+"/cmd/app" || len(build.Deps) != 5 {
		t.Fatalf("build = %+v, want the toolchain, the main package and five dependencies", build)
	}
	type tc struct {
		path string
		want self.ModuleValue
	}
	tests := []tc{
		{
			//: the stamp's full revision beats the pseudo-version's prefix, and
			//: "+dirty" is Modified even when the stamp says false.
			path: appPath,
			want: self.ModuleValue{
				Path: appPath, Revision: "8cf38860b6efa4146621893cc88085d66794a25c",
				Time: stampTime, Local: true, Modified: true,
			},
		},
		{path: frameworkMod, want: self.ModuleValue{Path: frameworkMod, Dir: "/src/framework", Local: true}},
		{path: sdkMod, want: self.ModuleValue{Path: sdkMod, Version: "v0.4.6"}},
		{
			path: "example.com/pseudo",
			want: self.ModuleValue{
				Path: "example.com/pseudo", Revision: "23e4c32e7484",
				Time: time.Date(2026, 9, 24, 10, 2, 34, 0, time.UTC),
			},
		},
		{path: "example.com/forked", want: self.ModuleValue{Path: "example.com/forked", Version: "v1.0.1", Replacement: "example.com/fork"}},
		{path: "example.com/workspace", want: self.ModuleValue{Path: "example.com/workspace", Local: true}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got, found := build.Module(c.path)
		if !found {
			t.Fatalf("Module(%q) not found", c.path)
		}
		if got != c.want {
			t.Errorf("Module(%q) =\n  %+v\nwant\n  %+v", c.path, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.path, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
	if _, found := build.Module("example.com/absent"); found {
		t.Error("Module found a path the build does not contain")
	}
}

// TestParseBuildReadsAReleaseAndABuildOutsideVersionControl covers the two
// main-module shapes the stamp is absent from: `go install app@v1.2.3`, which
// builds a release from the module cache, and a build with no VCS, which Go
// records as "(devel)".
func TestParseBuildReadsAReleaseAndABuildOutsideVersionControl(t *testing.T) {
	t.Parallel()
	release := self.ParseBuild(&debug.BuildInfo{Main: debug.Module{Path: appPath, Version: "v1.2.3"}})
	if release.Main != (self.ModuleValue{Path: appPath, Version: "v1.2.3"}) {
		t.Errorf("an installed release reads %+v", release.Main)
	}
	devel := self.ParseBuild(&debug.BuildInfo{Main: debug.Module{Path: appPath, Version: "(devel)"}})
	if devel.Main != (self.ModuleValue{Path: appPath, Local: true}) {
		t.Errorf("a build outside version control reads %+v", devel.Main)
	}
	dirtyRelease := self.ParseBuild(&debug.BuildInfo{Main: debug.Module{Path: appPath, Version: "v1.2.3+dirty"}})
	if dirtyRelease.Main.Version != "v1.2.3" || !dirtyRelease.Main.Modified {
		t.Errorf("a modified tree at a tag reads %+v", dirtyRelease.Main)
	}
	if empty := self.ParseBuild(nil); empty.Main != (self.ModuleValue{}) || empty.Deps != nil {
		t.Errorf("ParseBuild(nil) = %+v, want the zero value", empty)
	}
}

// TestReadBuildAgreesWithTheToolchain pins that ReadBuild is ParseBuild over
// the running binary's own information, whatever built this test binary.
func TestReadBuildAgreesWithTheToolchain(t *testing.T) {
	t.Parallel()
	info, embedded := debug.ReadBuildInfo()
	build, read := self.ReadBuild()
	if read != embedded {
		t.Fatalf("ReadBuild reported %v, the toolchain %v", read, embedded)
	}
	if !embedded {
		return
	}
	if build.Main.Path != info.Main.Path || build.GoVersion != info.GoVersion || len(build.Deps) != len(info.Deps) {
		t.Errorf("ReadBuild() = %+v, disagrees with debug.ReadBuildInfo()", build)
	}
}
