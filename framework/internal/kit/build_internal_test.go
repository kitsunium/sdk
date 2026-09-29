package kit

import (
	"runtime/debug"
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/git"
)

func TestBuildOfATodoOnALocalKit(t *testing.T) {
	bi := &debug.BuildInfo{
		Main: debug.Module{Path: "github.com/kitsunium/todo", Version: "v0.0.0-20260924095948-8cf38860b6ef"},
		Deps: []*debug.Module{
			{Path: frameworkModule, Version: "v0.0.0", Replace: &debug.Module{Path: "/src/platform", Version: "(devel)"}},
			{Path: sdkModule, Version: "v0.4.6"},
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
	if !b.Kit.Local || b.Kit.Version != "" || b.Kit.Revision != "814a8d1c0ffee" || b.Kit.Time == nil || !b.Kit.Time.Equal(at) || asked != "/src/platform" {
		t.Errorf("kit %+v (git asked about %q)", b.Kit, asked)
	}
	if b.SDK.Version != "v0.4.6" || b.SDK.Local {
		t.Errorf("sdk %+v", b.SDK)
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
