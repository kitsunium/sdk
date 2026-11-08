package buildinfo_test

import (
	"runtime"
	"runtime/debug"
	"slices"
	"testing"

	"github.com/kitsunium/sdk/internal/service/proc/buildinfo"
)

// TestParseBuildToolchain pins that the settings describing the toolchain are
// read into Toolchain, by key, and that Settings keeps every pair verbatim and
// in the toolchain's order.
func TestParseBuildToolchain(t *testing.T) {
	t.Parallel()
	settings := []debug.BuildSetting{
		{Key: "-trimpath", Value: "true"},
		{Key: "-tags", Value: "a,b"},
		{Key: "CGO_ENABLED", Value: "0"},
		{Key: "GOAMD64", Value: "v3"},
		{Key: "DefaultGODEBUG", Value: "x=1"},
	}
	build := buildinfo.ParseBuild(&debug.BuildInfo{Path: appPath, Main: debug.Module{Path: appPath}, Settings: settings})
	want := buildinfo.ToolchainValue{ArchLevel: "v3", DefaultGODEBUG: "x=1", Tags: []string{"a", "b"}, Trimpath: true}
	got := build.Toolchain
	//: every field the five settings name, and none they do not.
	if got.ArchLevel != want.ArchLevel || got.DefaultGODEBUG != want.DefaultGODEBUG ||
		!slices.Equal(got.Tags, want.Tags) || got.Trimpath != want.Trimpath || got.CgoEnabled ||
		got.OS != "" || got.Arch != "" || got.Compiler != "" || got.BuildMode != "" || got.LDFlags != "" {
		t.Fatalf("Toolchain = %+v, want %+v", got, want)
	}
	//: the five pairs, verbatim and in order.
	if len(build.Settings) != len(settings) {
		t.Fatalf("Settings = %+v, want the %d pairs", build.Settings, len(settings))
	}
	for i, setting := range settings {
		//: the same key and value at the same place.
		if build.Settings[i] != (buildinfo.SettingValue{Key: setting.Key, Value: setting.Value}) {
			t.Fatalf("Settings[%d] = %+v, want %+v", i, build.Settings[i], setting)
		}
	}
}

// TestParseBuildToolchainEveryKey pins the remaining keys: the target, the
// compiler, the build mode, cgo enabled and the linker flags.
func TestParseBuildToolchainEveryKey(t *testing.T) {
	t.Parallel()
	build := buildinfo.ParseBuild(&debug.BuildInfo{Settings: []debug.BuildSetting{
		{Key: "-buildmode", Value: "exe"},
		{Key: "-compiler", Value: "gc"},
		{Key: "-ldflags", Value: "-s -w -X main.v=1"},
		{Key: "CGO_ENABLED", Value: "1"},
		{Key: "GOARCH", Value: "arm"},
		{Key: "GOARM", Value: "7"},
		{Key: "GOOS", Value: "linux"},
		{Key: "-tags", Value: ""},
	}})
	want := buildinfo.ToolchainValue{
		OS: "linux", Arch: "arm", ArchLevel: "7", Compiler: "gc", BuildMode: "exe",
		CgoEnabled: true, LDFlags: "-s -w -X main.v=1",
	}
	got := build.Toolchain
	//: an empty -tags is no tag, not one empty tag.
	if got.OS != want.OS || got.Arch != want.Arch || got.ArchLevel != want.ArchLevel ||
		got.Compiler != want.Compiler || got.BuildMode != want.BuildMode ||
		got.CgoEnabled != want.CgoEnabled || got.LDFlags != want.LDFlags || len(got.Tags) != 0 || got.Trimpath {
		t.Fatalf("Toolchain = %+v, want %+v", got, want)
	}
}

// TestReproducible holds Reproducible to its three conditions over the eight
// combinations: only -trimpath, a revision and no modification together answer
// true.
func TestReproducible(t *testing.T) {
	t.Parallel()
	type tc struct {
		trimpath, revision, modified bool
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var settings []debug.BuildSetting
		//: the flag as the toolchain writes it.
		if c.trimpath {
			settings = append(settings, debug.BuildSetting{Key: "-trimpath", Value: "true"})
		}
		//: the stamp of a checkout.
		if c.revision {
			settings = append(settings, debug.BuildSetting{Key: "vcs.revision", Value: "8cf38860b6efa4146621893cc88085d66794a25c"})
		}
		modified := "false"
		//: uncommitted changes.
		if c.modified {
			modified = "true"
		}
		settings = append(settings, debug.BuildSetting{Key: "vcs.modified", Value: modified})
		build := buildinfo.ParseBuild(&debug.BuildInfo{Main: debug.Module{Path: appPath}, Settings: settings})
		want := c.trimpath && c.revision && !c.modified
		//: the conjunction, and nothing else.
		if got := build.Reproducible(); got != want {
			t.Fatalf("Reproducible() = %v, want %v for %+v", got, want, c)
		}
	}
	for _, trimpath := range []bool{false, true} {
		for _, revision := range []bool{false, true} {
			for _, modified := range []bool{false, true} {
				c := tc{trimpath: trimpath, revision: revision, modified: modified}
				t.Run("", func(t *testing.T) { t.Parallel(); runCase(t, c) })
			}
		}
	}
}

// TestReadBuildToolchainTarget pins the running test binary's own target.
func TestReadBuildToolchainTarget(t *testing.T) {
	t.Parallel()
	build, ok := buildinfo.ReadBuild()
	//: a test binary built by Bazel or the go command without module support.
	if !ok {
		t.Skip("the test binary carries no build information")
	}
	stamped := slices.ContainsFunc(build.Settings, func(s buildinfo.SettingValue) bool { return s.Key == "GOOS" })
	//: the go command always stamps the target; a rules_go build may not.
	if !stamped {
		t.Skip("the test binary carries no GOOS setting (a Bazel build)")
	}
	//: the toolchain's target is the one the binary runs on.
	if build.Toolchain.OS != runtime.GOOS || build.Toolchain.Arch != runtime.GOARCH {
		t.Fatalf("Toolchain = %s/%s, want %s/%s", build.Toolchain.OS, build.Toolchain.Arch, runtime.GOOS, runtime.GOARCH)
	}
}
