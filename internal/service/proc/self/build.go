package self

import (
	"runtime/debug"
	"strings"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/semver"
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
// IsPseudoVersion has already recognised the shape, so the revision always
// reads; the time does not when the stamp's fourteen digits are no instant —
// a thirteenth month — and is then left zero, which is the honest reading of
// a time that does not parse.
func pseudoCommit(version string) (revision string, at time.Time) {
	//: the twelve-character commit prefix.
	if rev, ok := semver.PseudoVersionRev(version); ok {
		revision = rev
	}
	//: the commit's time, to the second, in UTC.
	if stamp, ok := semver.PseudoVersionTime(version); ok {
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
	case semver.IsPseudoVersion(version):
		described.Revision, described.Time = pseudoCommit(version)
	//: a release.
	default:
		described.Version = version
	}
	//: the version, read.
	return described
}
