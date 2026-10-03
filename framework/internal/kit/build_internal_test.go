package kit

import (
	"io"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/git"
	"github.com/kitsunium/sdk/framework/model"
)

func TestBuildOfATodoOnALocalKit(t *testing.T) {
	// The replacement is an absolute directory of this OS: "/src/sdk" is not
	// one on Windows, and a relative one is never asked of git.
	local := filepath.Join(t.TempDir(), "sdk")
	bi := &debug.BuildInfo{
		Main: debug.Module{Path: "github.com/kitsunium/todo", Version: "v0.0.0-20260924095948-8cf38860b6ef"},
		Deps: []*debug.Module{
			{Path: sdkModule, Version: "v0.0.0", Replace: &debug.Module{Path: local, Version: "(devel)"}},
		},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "8cf38860b6efa4146621893cc88085d66794a25c"},
			{Key: "vcs.time", Value: "2026-09-24T09:59:48Z"},
			{Key: "vcs.modified", Value: "true"},
		},
	}
	at := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	asked := ""
	head := func(dir string) (git.HeadState, bool) {
		asked = dir
		return git.HeadState{Revision: "814a8d1c0ffee", Time: at}, true
	}
	b := buildOf(bi, head)
	if b.Product.Version != "" || !strings.HasPrefix(b.Product.Revision, "8cf3886") || !b.Product.Modified || b.Product.Time == nil || b.Product.Local {
		t.Errorf("product %+v: a pseudo-version is not a version; the VCS stamp is the revision", b.Product)
	}
	if !b.Kit.Local || b.Kit.Version != "" || b.Kit.Revision != "814a8d1c0ffee" || b.Kit.Time == nil || !b.Kit.Time.Equal(at) || asked != local {
		t.Errorf("kit %+v (git asked about %q)", b.Kit, asked)
	}
	// kit is a part of the SDK module (ADR 0162): the SDK says what kit says,
	// in a copy of its own.
	if b.SDK.Module != sdkModule || !b.SDK.Local || b.SDK.Revision != b.Kit.Revision || b.SDK.Time == nil || b.SDK.Time == b.Kit.Time {
		t.Errorf("sdk %+v, want kit's %+v in a copy of its own", b.SDK, b.Kit)
	}
	// Outside dev, nothing runs git: the local module has no revision.
	if b := buildOf(bi, nil); !b.Kit.Local || b.Kit.Revision != "" {
		t.Errorf("without git, kit %+v", b.Kit)
	}
}

func TestDevBuildOfTheEnvironment(t *testing.T) {
	env := map[string]string{
		envDevBuiltAt: "2026-09-24T10:00:00.5Z",
		envDevBuildMs: "1850",
		envDevBuilds:  "7",
		envDevChanged: "identity/api.go\n../../etc/passwd\n/abs/path.go\nnotify/locales/fr.json",
		envDevMore:    "5",
	}
	d := devBuildOf(func(k string) string { return env[k] })
	if d == nil || d.BuildMs != 1850 || d.Rebuilds != 7 || d.More != 5 || d.BuiltAt == nil {
		t.Fatalf("dev build %+v", d)
	}
	if len(d.Changed) != 2 || d.Changed[0] != "identity/api.go" || d.Changed[1] != "notify/locales/fr.json" {
		t.Errorf("only module-relative paths are kept: %v", d.Changed)
	}
	if devBuildOf(func(string) string { return "" }) != nil {
		t.Error("a process kit dev did not start has no dev build")
	}
}

func TestAServiceDocSpeaksItsLanguages(t *testing.T) {
	svc := NewService("bilingual", "Accounts and sessions.\n\nfr: Comptes et sessions.")
	app := NewApp("bilingual", svc).With(InMemory())
	if err := app.resolve(); err != nil {
		t.Fatal(err)
	}
	n := app.Graph().Node("bilingual")
	if n == nil || n.Doc != "Accounts and sessions." || n.Docs["fr"] != "Comptes et sessions." {
		t.Fatalf("service node %+v", n)
	}
}

// A product on a released SDK says that release for kit and for the SDK: they
// are one module (ADR 0162).
func TestKitIsTheSDKsRelease(t *testing.T) {
	bi := &debug.BuildInfo{
		Main: debug.Module{Path: "github.com/kitsunium/todo", Version: "(devel)"},
		Deps: []*debug.Module{{Path: sdkModule, Version: "v0.18.0"}},
	}
	b := buildOf(bi, nil)
	for name, m := range map[string]model.ModuleVersion{"kit": b.Kit, "sdk": b.SDK} {
		if m.Module != sdkModule || m.Version != "v0.18.0" || m.Local {
			t.Errorf("%s %+v, want %s v0.18.0", name, m, sdkModule)
		}
	}
}

// A pseudo-version names a commit, not a release: the SDK's reading of the
// build says so, and kit shows the commit.
func TestPseudoVersionsAreCommits(t *testing.T) {
	bi := &debug.BuildInfo{
		Main: debug.Module{Path: "github.com/kitsunium/todo", Version: "(devel)"},
		Deps: []*debug.Module{{Path: sdkModule, Version: "v0.4.7-0.20260924100234-abcdefabcdef"}},
	}
	if m := buildOf(bi, nil).SDK; m.Version != "" || m.Revision != "abcdefabcdef" || m.Time == nil || m.Local {
		t.Errorf("a pseudo-versioned dependency is a commit: %+v", m)
	}
}

// Every app of a process gets its own copy of the build: the process reads
// and describes it once, and a caller that edits what one graph returns
// changes no other graph, nor what telemetry reports.
func TestEachAppOwnsItsBuild(t *testing.T) {
	first := NewApp("first-build", NewService("first-build", "A first product.")).With(InMemory(), Env(EnvProduction), Logs(io.Discard))
	second := NewApp("second-build", NewService("second-build", "A second product.")).With(InMemory(), Env(EnvProduction), Logs(io.Discard))
	for _, a := range []*App{first, second} {
		if err := a.resolve(); err != nil {
			t.Fatal(err)
		}
	}
	b1 := first.Graph().App.Build
	if b1 == nil {
		t.Fatal("a production app describes no build: the test binary carries build information")
	}
	b1.Product.Module, b1.Kit.Version = "edited", "edited"
	if b2 := second.Graph().App.Build; b2 == nil || b2.Product.Module == "edited" || b2.Kit.Version == "edited" {
		t.Errorf("an edit of one graph's build reached another app's: %+v", b2)
	}
	if again := first.Graph().App.Build; again.Product.Module == "edited" {
		t.Errorf("an edit of a graph's build reached the app's next graph: %+v", again)
	}
}

// A copy of a build shares no instant with it.
func TestACopiedBuildSharesNothing(t *testing.T) {
	at := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	b := &model.Build{Product: model.ModuleVersion{Module: "p", Time: &at}, Kit: model.ModuleVersion{Time: &at}, SDK: model.ModuleVersion{Time: &at}}
	c := cloneBuild(b)
	*c.Product.Time, *c.Kit.Time, *c.SDK.Time = time.Time{}, time.Time{}, time.Time{}
	c.Product.Module = "q"
	if !b.Product.Time.Equal(at) || !b.Kit.Time.Equal(at) || !b.SDK.Time.Equal(at) || b.Product.Module != "p" {
		t.Errorf("the copy shares with its build: %+v", b)
	}
	if cloneBuild(nil) != nil {
		t.Error("a copy of no build is one")
	}
}
