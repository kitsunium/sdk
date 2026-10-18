package app

import (
	"context"
	"path/filepath"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kitsunium/sdk/framework/git"
	"github.com/kitsunium/sdk/framework/internal/app/plug"
	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/proc/buildinfo"
)

// sdkModule is the module a build's kit and SDK are read from, besides the
// product's own. The framework and the SDK are one Go module (ADR 0162), so
// the two always say the same version; the graph keeps both, as it always
// had them.
const sdkModule = "github.com/kitsunium/sdk"

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
		if bi.Main.Path == sdkModule {
			return bi.Main.Version
		}
		for _, d := range bi.Deps {
			if d.Path == sdkModule {
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
	parsedBuild = sync.OnceValue(func() *buildinfo.BuildInfo {
		bi := readBuild()
		if bi == nil {
			return nil
		}
		return new(buildinfo.ParseBuild(bi))
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
	out.Product, out.App, out.SDK = cloneVersion(b.Product), cloneVersion(b.App), cloneVersion(b.SDK)
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
// it (buildinfo.ParseBuild): the product's own module with the VCS stamp Go
// records, and the version of the SDK module, which holds kit (ADR 0162) — so
// Kit and SDK say the same module at the same version. A module built from a
// local directory — a workspace, a replace — has no version; with git (dev
// only) its commit is still told.
func buildOf(bi *debug.BuildInfo, head gitHead) *model.Build {
	if bi == nil {
		return nil
	}
	info := buildinfo.ParseBuild(bi)
	b := &model.Build{
		Product: versionOf(&info.Main, nil),
		App:     model.ModuleVersion{Module: sdkModule},
	}
	// A product is always built from its own tree: its commit says where.
	b.Product.Local = false
	if info.Main.Path == sdkModule {
		b.App = b.Product
	} else if m, ok := info.Module(sdkModule); ok {
		b.App = versionOf(&m, head)
	}
	// Its own copy: a caller that edits one of the two edits only that one.
	b.SDK = cloneVersion(b.App)
	return b
}

// versionOf says one module as the model does: its release, or the commit it
// came from — which git tells, when head is given, for a directory the
// toolchain does not stamp.
func versionOf(m *buildinfo.Module, head gitHead) model.ModuleVersion {
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

// devBuildOf is what kit dev said about the build it launched, as the
// Studio subsystem reads it (plug.StudioDriverConfig.DevBuild), or nil: when
// the process was not started by kit dev, and in a program that does not
// import framework/app/studio, whose core reads none of the tool's
// variables (ADR 0180 step 1).
func devBuildOf(getenv func(string) string) *model.DevBuild {
	d := plug.Studio.Load()
	if d == nil {
		return nil
	}
	return d.DevBuild(getenv)
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

// buildDescription is an app's build, described once, at its first read
// (#276): a run that never reads it — a CLI command, a daemon nobody asks —
// never parses the binary's build information, which every redraw of a
// status line would otherwise pay. In dev it is described at the start, so
// asking git about the local modules stays the start's cost, where kit dev
// measures it. A nil description describes nothing.
type buildDescription struct {
	once     sync.Once
	done     atomic.Bool
	isDev    bool
	getenv   func(string) string
	build    *model.Build
	devBuild *model.DevBuild
}

// newBuildDescription is the build of an app in dev or not, described now in
// dev and at the first read otherwise.
func newBuildDescription(dev bool, getenv func(string) string) *buildDescription {
	d := &buildDescription{isDev: dev, getenv: getenv}
	if dev {
		d.describe()
	}
	return d
}

// describe runs describeBuild once.
func (d *buildDescription) describe() {
	d.once.Do(func() {
		d.build, d.devBuild = describeBuild(d.isDev, d.getenv)
		d.done.Store(true)
	})
}

// product is what the binary was built from; nil when it carries no build
// information, or for a nil description.
func (d *buildDescription) product() *model.Build {
	if d == nil {
		return nil
	}
	d.describe()
	return d.build
}

// dev is what kit dev said about the build it launched; nil outside dev.
func (d *buildDescription) dev() *model.DevBuild {
	if d == nil {
		return nil
	}
	d.describe()
	return d.devBuild
}

// described reports whether the build was described yet.
func (d *buildDescription) described() bool { return d != nil && d.done.Load() }

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
