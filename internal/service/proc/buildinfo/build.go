package buildinfo

import (
	"runtime/debug"
	"slices"
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
	// The settings Go stamps for the toolchain that built the binary.
	settingOS          string = "GOOS"
	settingArch        string = "GOARCH"
	settingCompiler    string = "-compiler"
	settingBuildMode   string = "-buildmode"
	settingTags        string = "-tags"
	settingLDFlags     string = "-ldflags"
	settingGODEBUG     string = "DefaultGODEBUG"
	settingCgoEnabled  string = "CGO_ENABLED"
	settingTrimpath    string = "-trimpath"
	settingCgoOn       string = "1"
	settingTagsJoining string = ","
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
	// Toolchain is how the toolchain built the binary: the target, the
	// compiler, the flags and the cgo setting, read from the build settings.
	Toolchain ToolchainValue
	// Settings are every build setting the toolchain recorded, verbatim and in
	// its order — the vcs stamp and the toolchain's included, and a key a later
	// toolchain adds that Toolchain does not name.
	Settings []SettingValue
}

// SettingValue is one build setting as the toolchain recorded it: a key such
// as "-trimpath", "CGO_ENABLED" or "vcs.revision", and its value.
type SettingValue struct {
	// Key names the setting, e.g. "-tags" or "GOAMD64".
	Key string
	// Value is the setting's value as recorded, e.g. "a,b" or "v3".
	Value string
}

// ToolchainValue is how the toolchain built the binary, read by key from the
// build settings it recorded. A setting the toolchain did not record leaves
// its field zero: a build by the go command records the target and the
// compiler, and one by Bazel records none of them.
type ToolchainValue struct {
	// OS is the target operating system, GOOS, e.g. "linux".
	OS string
	// Arch is the target architecture, GOARCH, e.g. "amd64".
	Arch string
	// ArchLevel is the architecture's feature level — GOAMD64, GOARM, GO386,
	// GOARM64 and their siblings — e.g. "v3", which needs AVX2 on the host.
	ArchLevel string
	// Compiler is the compiler toolchain, -compiler, e.g. "gc".
	Compiler string
	// BuildMode is the -buildmode flag, e.g. "exe" or "pie".
	BuildMode string
	// Tags are the build tags, -tags, in the order given; none when the build
	// named none.
	Tags []string
	// LDFlags is the -ldflags flag as given, e.g. "-s -w -X main.v=1".
	LDFlags string
	// DefaultGODEBUG is the GODEBUG default the binary runs with before its
	// environment's, e.g. "httpmuxgo121=1".
	DefaultGODEBUG string
	// CgoEnabled reports that CGO_ENABLED was 1.
	CgoEnabled bool
	// Trimpath reports that -trimpath was set: file paths in panics and
	// profiles are module paths, not the build machine's directories.
	Trimpath bool
}

var (
	// toolchainSettings reads each toolchain key the go command records into its
	// field: a table, not a switch, so a key a later toolchain adds is one line.
	toolchainSettings = map[string]func(*ToolchainValue, string){
		settingOS:        func(d *ToolchainValue, v string) { d.OS = v },
		settingArch:      func(d *ToolchainValue, v string) { d.Arch = v },
		settingCompiler:  func(d *ToolchainValue, v string) { d.Compiler = v },
		settingBuildMode: func(d *ToolchainValue, v string) { d.BuildMode = v },
		//: the tags, comma-joined by the go command; an empty flag is no tag.
		settingTags:    func(d *ToolchainValue, v string) { d.Tags = splitTags(v) },
		settingLDFlags: func(d *ToolchainValue, v string) { d.LDFlags = v },
		settingGODEBUG: func(d *ToolchainValue, v string) { d.DefaultGODEBUG = v },
		//: cgo, which the go command records as 0 or 1.
		settingCgoEnabled: func(d *ToolchainValue, v string) { d.CgoEnabled = v == settingCgoOn },
		//: -trimpath, recorded only when set.
		settingTrimpath: func(d *ToolchainValue, v string) { d.Trimpath = v == settingTrue },
	}

	// archLevelSettings are the keys the go command records the target
	// architecture's feature level under — one per GOARCH that has one
	// (cmd/go/internal/cfg.GetArchEnv).
	archLevelSettings = [...]string{
		"GO386", "GOAMD64", "GOARM", "GOARM64", "GOMIPS", "GOMIPS64", "GOPPC64", "GORISCV64", "GOWASM",
	}
)

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
	// "github.com/kitsunium/sdk".
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

// Reproducible reports whether the build can be made again byte for byte
// from its source: built with -trimpath, from a recorded revision, with no
// uncommitted change. It is false for a build that does not say — one
// outside version control, or a Bazel build, which stamps none of the
// three.
func (b *BuildValue) Reproducible() bool {
	//: the three conditions together.
	return b.Toolchain.Trimpath && b.Main.Revision != "" && !b.Main.Modified
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
		Settings:  make([]SettingValue, 0, len(info.Settings)),
	}
	//: every setting, verbatim, and the toolchain's read by key.
	for _, setting := range info.Settings {
		build.Settings = append(build.Settings, SettingValue{Key: setting.Key, Value: setting.Value})
		toolchain(&build.Toolchain, setting)
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

// toolchain applies one build setting to the toolchain's description; a
// setting that does not describe the toolchain leaves it unchanged.
func toolchain(described *ToolchainValue, setting debug.BuildSetting) {
	//: a key the go command records about how it built.
	if apply, known := toolchainSettings[setting.Key]; known {
		apply(described, setting.Value)
		return
	}
	//: the feature level, under the key its architecture names: GOAMD64,
	//: GOARM, GO386 and their siblings.
	if slices.Contains(archLevelSettings[:], setting.Key) {
		described.ArchLevel = setting.Value
	}
}

// splitTags reads the go command's comma-joined -tags value.
func splitTags(value string) []string {
	//: no tag at all, rather than one empty tag.
	if value == "" {
		//: none.
		return nil
	}
	//: in the order given.
	return strings.Split(value, settingTagsJoining)
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
