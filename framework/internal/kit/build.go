// Package kit — the build a graph describes: the modules and versions it was
// made of.
package kit

import (
	"context"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/git"
	"github.com/kitsunium/sdk/pkg/v1/process"
)

// The modules a build is described by, besides the product's own.
const (
	frameworkModule = "github.com/kitsunium/sdk/framework"
	sdkModule       = "github.com/kitsunium/sdk/pkg"
)

// Environment kit dev gives the process it launches, describing the build.
const (
	envDevBuiltAt     = "KIT_DEV_BUILT_AT"
	envDevBuildMs     = "KIT_DEV_BUILD_MS"
	envDevBuilds      = "KIT_DEV_REBUILDS"
	envDevChanged     = "KIT_DEV_CHANGED"
	envDevMore        = "KIT_DEV_CHANGED_MORE"
	maxDevChanged int = 20
)

var (
	// gitCache keeps git's answer per directory for the life of the process:
	// kit dev restarts the process on every rebuild, and a test starts many
	// apps.
	gitCache sync.Map // dir → gitAnswer
	// kitVersion is the version of this module in the product's build, read
	// once.
	kitVersion = sync.OnceValue(func() string {
		bi := readBuild()
		if bi == nil {
			return "(unknown)"
		}
		if bi.Main.Path == frameworkModule {
			return bi.Main.Version
		}
		for _, d := range bi.Deps {
			if d.Path == frameworkModule {
				if d.Replace != nil {
					return "(replaced)"
				}
				return d.Version
			}
		}
		return "(devel)"
	})
	// readBuild is the running binary's build information, read once: the
	// runtime parses it anew at every debug.ReadBuildInfo.
	readBuild = sync.OnceValue(func() *debug.BuildInfo {
		bi, _ := debug.ReadBuildInfo()
		return bi
	})
	// parsedBuild is readBuild as the SDK reads it, parsed once; nil when the
	// binary carries no build information.
	parsedBuild = sync.OnceValue(func() *process.BuildInfo {
		bi := readBuild()
		if bi == nil {
			return nil
		}
		return new(process.ParseBuild(bi))
	})
	// productionBuild is the build as a production run describes it — no
	// question to git —, described once for the process; every app gets its
	// own copy (cloneBuild), which a caller of App.Graph may edit.
	productionBuild = sync.OnceValue(func() *model.Build { return buildOf(readBuild(), nil) })
)

// cloneBuild is a copy of b that shares nothing with it; nil for nil.
func cloneBuild(b *model.Build) *model.Build {
	if b == nil {
		return nil
	}
	out := *b
	out.Product, out.Kit, out.SDK = cloneVersion(b.Product), cloneVersion(b.Kit), cloneVersion(b.SDK)
	return &out
}

// cloneVersion is a copy of v that shares nothing with it.
func cloneVersion(v model.ModuleVersion) model.ModuleVersion {
	if v.Time != nil {
		v.Time = new(*v.Time)
	}
	return v
}

// gitHead asks git where a local module's directory is: its commit, the
// commit's time, and whether tracked files differ from it.
type gitHead func(dir string) (git.HeadState, bool)

type gitAnswer struct {
	head git.HeadState
	ok   bool
}

// buildOf describes what the running binary was built from, as the SDK reads
// it (process.ParseBuild): the product's own module with the VCS stamp Go
// records, and the versions of kit and of the SDK. A module built from a
// local directory — a workspace, a replace — has no version; with git (dev
// only) its commit is still told.
func buildOf(bi *debug.BuildInfo, head gitHead) *model.Build {
	if bi == nil {
		return nil
	}
	info := process.ParseBuild(bi)
	b := &model.Build{
		Product: versionOf(&info.Main, nil),
		Kit:     model.ModuleVersion{Module: frameworkModule},
		SDK:     model.ModuleVersion{Module: sdkModule},
	}
	// A product is always built from its own tree: its commit says where.
	b.Product.Local = false
	if info.Main.Path == frameworkModule {
		b.Kit = b.Product
	} else if m, ok := info.Module(frameworkModule); ok {
		b.Kit = versionOf(&m, head)
	}
	if m, ok := info.Module(sdkModule); ok {
		b.SDK = versionOf(&m, head)
	}
	return b
}

// versionOf says one module as the model does: its release, or the commit it
// came from — which git tells, when head is given, for a directory the
// toolchain does not stamp.
func versionOf(m *process.Module, head gitHead) model.ModuleVersion {
	v := model.ModuleVersion{Module: m.Path, Version: m.Version, Revision: m.Revision, Modified: m.Modified, Local: m.Local}
	at := m.Time
	if m.Local && head != nil && filepath.IsAbs(m.Dir) {
		if h, ok := head(m.Dir); ok {
			v.Revision, v.Modified, at = h.Revision, h.Modified, h.Time
		}
	}
	if !at.IsZero() {
		v.Time = &at
	}
	return v
}

// headAt asks the SDK's git about a local module's directory, bounded in
// time: a machine without git, or a directory outside any repository, just
// goes without.
func headAt(dir string) (git.HeadState, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	h, err := git.Head(ctx, dir)
	return h, err == nil
}

// devBuildOf reads what kit dev said about the build it launched, or nil
// when the process was not started by kit dev.
func devBuildOf(getenv func(string) string) *model.DevBuild {
	at, err := time.Parse(time.RFC3339Nano, getenv(envDevBuiltAt))
	if err != nil {
		return nil
	}
	d := &model.DevBuild{BuiltAt: new(at.UTC()), Changed: changedFiles(getenv(envDevChanged))}
	if ms, err := strconv.ParseFloat(getenv(envDevBuildMs), 64); err == nil && ms >= 0 {
		d.BuildMs = round2(ms)
	}
	d.Rebuilds, d.More = positiveInt(getenv(envDevBuilds)), positiveInt(getenv(envDevMore))
	return d
}

// changedFiles are the files a dev build says changed, one a line: local
// paths only, at most maxDevChanged.
func changedFiles(raw string) []string {
	var out []string
	for f := range strings.SplitSeq(raw, "\n") {
		if f = strings.TrimSpace(f); f != "" && filepath.IsLocal(filepath.FromSlash(f)) && len(out) < maxDevChanged {
			out = append(out, f)
		}
	}
	return out
}

// positiveInt is raw as a positive integer, 0 when it is not one.
func positiveInt(raw string) int {
	if n, err := strconv.Atoi(raw); err == nil && n > 0 {
		return n
	}
	return 0
}

// cachedHeadAt is headAt(dir), asked of git once per directory for the
// process.
func cachedHeadAt(dir string) (git.HeadState, bool) {
	if v, ok := gitCache.Load(dir); ok {
		if g, isAnswer := v.(gitAnswer); isAnswer {
			return g.head, g.ok
		}
	}
	h, ok := headAt(dir)
	gitCache.Store(dir, gitAnswer{head: h, ok: ok})
	return h, ok
}

// describeBuild is the build and, in dev, kit dev's account of it. Only dev
// asks git about local modules: a production binary never runs a command,
// and describes its build once per process.
func describeBuild(dev bool, getenv func(string) string) (*model.Build, *model.DevBuild) {
	if !dev {
		return cloneBuild(productionBuild()), nil
	}
	return buildOf(readBuild(), cachedHeadAt), devBuildOf(getenv)
}

// moduleBuild is the Go module that holds package pkg, as the build says
// it: its release, or the commit it came from — git's answer, in dev, for a
// directory. nil when the binary carries no build information.
func (a *App) moduleBuild(pkg string) *model.ModuleVersion {
	m, ok := goModuleOf(pkg)
	if !ok {
		return nil
	}
	info := parsedBuild()
	if info == nil {
		return nil
	}
	mod, ok := info.Module(m.path)
	if !ok {
		return nil
	}
	var head gitHead
	if a.cfg.env == EnvDev {
		head = cachedHeadAt
	}
	return new(versionOf(&mod, head))
}
