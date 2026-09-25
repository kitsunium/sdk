// Package self is what the running process can say about itself: what it was
// built from (build.go) and what it is doing right now (stats.go).
//
// The rest of the proc domain acts on OTHER processes — it spawns, signals,
// reaps and limits children. This package only reads, and only the process it
// runs in: the build information the Go toolchain embedded in the binary, the
// Go runtime's own metrics, and the kernel's account of the CPU time used.
// Nothing here can fail in a way a caller could act on, so nothing returns an
// error: an absent answer is a zero field or a false, and the doc comment of
// each field says which.
package self

import (
	"runtime/debug"
	"strings"
	"time"

	"golang.org/x/mod/module"
)

// Values the Go toolchain writes into debug.BuildInfo.
const (
	// develVersion is the version of a module built from a directory that
	// carries none: a workspace module, a directory replacement, or a main
	// module built outside version control.
	develVersion string = "(devel)"
	// dirtySuffix is the build metadata Go appends to a main module's version
	// when its working tree had uncommitted changes (Go 1.24 and later).
	dirtySuffix string = "+dirty"
	// The settings Go stamps for a main module built inside a VCS checkout.
	settingRevision string = "vcs.revision"
	settingTime     string = "vcs.time"
	settingModified string = "vcs.modified"
	// settingVCSPrefix opens every one of them, and only them.
	settingVCSPrefix string = "vcs"
	// settingTrue is how a boolean setting spells true.
	settingTrue string = "true"
)

// BuildValue is what the running binary was built from: the toolchain, the
// main package, the main module with its version-control stamp, and every
// dependency with the replacement it was built through.
//
// It is the engine's own value (ADR 0074): no port in the proc domain speaks
// it, and it describes one mechanism's reading of runtime/debug.
type BuildValue struct {
	// GoVersion is the toolchain that built the binary, e.g. "go1.27.1".
	GoVersion string
	// Path is the import path of the main package, e.g.
	// "github.com/acme/app/cmd/server".
	Path string
	// Main is the main module.
	Main ModuleValue
	// Deps are the dependencies linked into the binary, in the order the
	// toolchain recorded them.
	Deps []ModuleValue
}

// ModuleValue is one module of the running binary, described by the code
// that was actually built for it.
//
// Its fields separate the three things a recorded version conflates: a
// RELEASE (Version), a COMMIT (Revision and Time), and a DIRECTORY on the
// build machine (Local and Dir). A pseudo-version names a commit rather than
// a release, so it fills Revision and Time and leaves Version empty; a module
// built from a directory has no version to tell at all.
type ModuleValue struct {
	// Time is the commit's time, when known: the main module's vcs.time
	// stamp, or the timestamp a pseudo-version carries. Zero otherwise.
	Time time.Time
	// Path is the module path the build required, e.g.
	// "github.com/kitsunium/sdk/pkg".
	Path string
	// Version is the released version the code came from, e.g. "v0.4.6", or
	// "" when there is none to tell: a pseudo-version (a commit, in Revision),
	// a module built from a local directory, or a main module built without
	// one. A "+dirty" suffix is not kept here; it is what Modified says.
	Version string
	// Revision is the version-control commit the code came from, when known:
	// the main module's vcs.revision stamp (a full object name), or the
	// commit a pseudo-version names (its twelve-character prefix).
	Revision string
	// Dir is the directory a replace directive pointed the module at, as the
	// toolchain recorded it — relative to the main module, or to the
	// workspace root in workspace mode — or "" when it was not replaced by a
	// directory. A workspace module is Local with no Dir: the toolchain does
	// not record where it was.
	Dir string
	// Replacement is the module path a replace directive substituted, when it
	// substituted another module rather than a directory; Version is then
	// that module's.
	Replacement string
	// Local reports that the code came from a directory on the build machine
	// rather than from a published version: a directory replacement, a
	// workspace module, or a main module built inside its own source tree.
	Local bool
	// Modified reports that the build included uncommitted changes: the main
	// module's vcs.modified stamp, or the "+dirty" its version carried. The
	// toolchain records nothing of the kind for a dependency.
	Modified bool
}

// Module returns the module whose path is path — the main module or one of
// the dependencies — and whether the build contains it.
func (b *BuildValue) Module(path string) (found ModuleValue, ok bool) {
	//: the main module answers for its own path.
	if b.Main.Path == path {
		//: the module being built.
		return b.Main, true
	}
	//: otherwise one of the dependencies, in recorded order.
	for _, dep := range b.Deps {
		//: the first match is the only one the toolchain records.
		if dep.Path == path {
			//: a dependency.
			return dep, true
		}
	}
	//: not linked into this binary.
	return ModuleValue{}, false
}

// ReadBuild describes the running binary. It reports false when the binary
// carries no build information — one built without module support — and
// never fails otherwise.
func ReadBuild() (build BuildValue, ok bool) {
	info, read := debug.ReadBuildInfo()
	//: nothing embedded, nothing to describe.
	if !read {
		//: the zero value and an honest false.
		return BuildValue{}, false
	}
	//: the same reading ParseBuild gives any BuildInfo.
	return ParseBuild(info), true
}

// ParseBuild describes the build info. It is what ReadBuild uses on the
// running binary, exported for a BuildInfo from elsewhere — one a test builds
// by hand, or debug.ParseBuildInfo's reading of another binary's `go version
// -m` output. A nil info describes nothing.
func ParseBuild(info *debug.BuildInfo) BuildValue {
	//: nothing to read.
	if info == nil {
		//: the zero value.
		return BuildValue{}
	}
	build := BuildValue{
		GoVersion: info.GoVersion,
		Path:      info.Path,
		Main:      mainModule(&info.Main, info.Settings),
		Deps:      make([]ModuleValue, 0, len(info.Deps)),
	}
	//: in the order the toolchain recorded them.
	for _, dep := range info.Deps {
		//: a nil entry is not something the toolchain writes, and is skipped
		//: rather than trusted.
		if dep == nil {
			continue
		}
		build.Deps = append(build.Deps, dependency(dep))
	}
	//: the whole build.
	return build
}

// mainModule describes the main module: its version as released or as a
// commit, then the version-control stamp, which is authoritative where it
// exists because it carries the full revision and the exact commit time.
func mainModule(main *debug.Module, settings []debug.BuildSetting) ModuleValue {
	described := fromVersion(main.Path, main.Version)
	//: a module built from its own checkout.
	described.Local = main.Version == develVersion || main.Version == ""
	//: the stamp Go writes when it built inside a VCS checkout.
	for _, setting := range settings {
		//: vcs, vcs.revision, vcs.time, vcs.modified: built from a source tree.
		if strings.HasPrefix(setting.Key, settingVCSPrefix) {
			described.Local = true
		}
		stamp(&described, setting)
	}
	//: the main module as the build describes it.
	return described
}

// stamp applies one version-control setting to the main module.
func stamp(described *ModuleValue, setting debug.BuildSetting) {
	switch setting.Key {
	//: the full object name, which beats a pseudo-version's prefix.
	case settingRevision:
		described.Revision = setting.Value
	//: the commit time, which beats a pseudo-version's second-granular copy.
	case settingTime:
		//: an unparsable stamp is left out rather than guessed.
		if at, err := time.Parse(time.RFC3339, setting.Value); err == nil {
			described.Time = at
		}
	//: uncommitted changes, which "+dirty" may already have said.
	case settingModified:
		described.Modified = described.Modified || setting.Value == settingTrue
	//: every other setting describes the toolchain, not the module.
	default:
	}
}

// dependency describes one dependency, following its replacement: the code
// built for it is the replacement's, so the version and the directory are too.
func dependency(dep *debug.Module) ModuleValue {
	replaced := dep.Replace
	//: not replaced: the dependency's own version, or a workspace module.
	if replaced == nil {
		described := fromVersion(dep.Path, dep.Version)
		//: a workspace module is built from a directory the toolchain does not
		//: name.
		described.Local = dep.Version == develVersion
		//: as required.
		return described
	}
	//: replaced by a directory: no version to tell, and the directory is.
	if replaced.Version == "" || replaced.Version == develVersion {
		//: local code, where the build said it was.
		return ModuleValue{Path: dep.Path, Dir: replaced.Path, Local: true}
	}
	//: replaced by another module: its version is what was built.
	described := fromVersion(dep.Path, replaced.Version)
	described.Replacement = replaced.Path
	//: the substitute's version under the required path.
	return described
}

// pseudoCommit returns the commit a pseudo-version names and its time.
// IsPseudoVersion has already validated both halves, so neither read fails in
// practice; one that did would leave its half empty, which is the honest
// reading of a version that does not parse.
func pseudoCommit(version string) (revision string, at time.Time) {
	//: the twelve-character commit prefix.
	if rev, revErr := module.PseudoVersionRev(version); revErr == nil {
		revision = rev
	}
	//: the commit's time, to the second, in UTC.
	if stamp, timeErr := module.PseudoVersionTime(version); timeErr == nil {
		at = stamp
	}
	//: whatever read.
	return revision, at
}

// fromVersion reads a recorded version into a release or a commit.
func fromVersion(path, version string) ModuleValue {
	described := ModuleValue{Path: path}
	//: "+dirty" is build metadata saying what Modified says; the version is
	//: what precedes it.
	if trimmed, dirty := strings.CutSuffix(version, dirtySuffix); dirty {
		version = trimmed
		described.Modified = true
	}
	switch {
	//: no version to tell.
	case version == develVersion || version == "":
		//: left empty.
	//: a pseudo-version names a commit, not a release.
	case module.IsPseudoVersion(version):
		described.Revision, described.Time = pseudoCommit(version)
	//: a release.
	default:
		described.Version = version
	}
	//: the version, read.
	return described
}
