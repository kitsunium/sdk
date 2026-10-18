package app

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
	if !b.App.Local || b.App.Version != "" || b.App.Revision != "814a8d1c0ffee" || b.App.Time == nil || !b.App.Time.Equal(at) || asked != local {
		t.Errorf("the framework %+v (git asked about %q)", b.App, asked)
	}
	// kit is a part of the SDK module (ADR 0162): the SDK says what kit says,
	// in a copy of its own.
	if b.SDK.Module != sdkModule || !b.SDK.Local || b.SDK.Revision != b.App.Revision || b.SDK.Time == nil || b.SDK.Time == b.App.Time {
		t.Errorf("sdk %+v, want the framework's %+v in a copy of its own", b.SDK, b.App)
	}
	// Outside dev, nothing runs git: the local module has no revision.
	if b := buildOf(bi, nil); !b.App.Local || b.App.Revision != "" {
		t.Errorf("without git, the framework %+v", b.App)
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
	for name, m := range map[string]model.ModuleVersion{"app": b.App, "sdk": b.SDK} {
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
	b1.Product.Module, b1.App.Version = "edited", "edited"
	if b2 := second.Graph().App.Build; b2 == nil || b2.Product.Module == "edited" || b2.App.Version == "edited" {
		t.Errorf("an edit of one graph's build reached another app's: %+v", b2)
	}
	if again := first.Graph().App.Build; again.Product.Module == "edited" {
		t.Errorf("an edit of a graph's build reached the app's next graph: %+v", again)
	}
}

// A copy of a build shares no instant with it.
func TestACopiedBuildSharesNothing(t *testing.T) {
	at := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	b := &model.Build{Product: model.ModuleVersion{Module: "p", Time: &at}, App: model.ModuleVersion{Time: &at}, SDK: model.ModuleVersion{Time: &at}}
	c := cloneBuild(b)
	*c.Product.Time, *c.App.Time, *c.SDK.Time = time.Time{}, time.Time{}, time.Time{}
	c.Product.Module = "q"
	if !b.Product.Time.Equal(at) || !b.App.Time.Equal(at) || !b.SDK.Time.Equal(at) || b.Product.Module != "p" {
		t.Errorf("the copy shares with its build: %+v", b)
	}
	if cloneBuild(nil) != nil {
		t.Error("a copy of no build is one")
	}
}

// A production run describes its build when something reads it, never at the
// start: a CLI command, or a daemon nobody asks, does not parse the binary's
// build information on every run (#276).
func TestAProductionBuildIsDescribedWhenRead(t *testing.T) {
	a := NewApp("lazy-build", NewService("lazy-build", "A product.")).With(InMemory(), Env(EnvProduction), Logs(io.Discard))
	if err := a.resolve(); err != nil {
		t.Fatal(err)
	}
	if a.build.described() {
		t.Fatal("the start described the build before anything read it")
	}
	if a.Graph().App.Build == nil {
		t.Fatal("a production app describes no build: the test binary carries build information")
	}
	if !a.build.described() {
		t.Error("the graph read a build that was never described")
	}
}
