package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// proxyStub serves a @v/list response, so no test touches the network.
func proxyStub(t *testing.T, versions ...string) probe {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/@v/list") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(strings.Join(versions, "\n") + "\n"))
	}))
	t.Cleanup(srv.Close)
	return probe{proxy: srv.URL, client: srv.Client()}
}

// modDir writes a go.mod and returns its directory.
func modDir(t *testing.T, gomod string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(gomod), 0o600); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	return dir
}

// Only strict releases are candidates. Suggesting a prerelease or a
// pseudo-version as "the latest" would be wrong advice, so they must not parse.
func TestSemverAcceptsOnlyStableReleases(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   string
		ok   bool
	}
	tests := []tc{
		{"release", "v0.1.24", true},
		{"zero", "v0.0.0", true},
		{"no v prefix", "0.1.24", false},
		{"prerelease", "v1.0.0-rc1", false},
		{"pseudo-version", "v0.0.0-20260101120000-abcdef123456", false},
		{"two components", "v1.2", false},
		{"negative", "v1.-2.0", false},
		{"non-numeric", "v1.x.0", false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if _, _, _, ok := parseSemver(c.in); ok != c.ok {
			t.Errorf("parseSemver(%q) ok = %v, want %v", c.in, ok, c.ok)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Ordering must be numeric, not lexical: v0.1.9 precedes v0.1.24, which a
// string comparison would get backwards — and getting it backwards would tell
// an up-to-date consumer to downgrade.
func TestSemverOrdersNumerically(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		a, b string
		want bool
	}
	tests := []tc{
		{"patch, double digits", "v0.1.9", "v0.1.24", true},
		{"patch, reversed", "v0.1.24", "v0.1.9", false},
		{"equal", "v1.2.3", "v1.2.3", false},
		{"minor wins over patch", "v0.1.99", "v0.2.0", true},
		{"major wins over minor", "v0.99.0", "v1.0.0", true},
		{"unparseable sorts first", "v0.0.0-2026-abcdef", "v0.1.0", true},
		{"nothing is newer than an unparseable target", "v0.1.0", "not-a-version", false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := semverLess(c.a, c.b); got != c.want {
			t.Errorf("semverLess(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// The go.mod reader must handle the shapes a real file uses.
func TestSDKRequirementReadsGoMod(t *testing.T) {
	t.Parallel()
	type tc struct {
		name        string
		gomod       string
		wantVersion string
		wantReplace bool
		wantFound   bool
	}
	tests := []tc{
		{
			"require block",
			"module x\n\ngo 1.27\n\nrequire (\n\tgithub.com/kitsunium/sdk/pkg v0.1.24\n)\n",
			"v0.1.24", false, true,
		},
		{
			"single-line require",
			"module x\n\nrequire github.com/kitsunium/sdk/pkg v0.1.20\n",
			"v0.1.20", false, true,
		},
		{
			// A replaced module is someone working against a local checkout;
			// telling them to upgrade would be noise.
			"replace directive suppresses the check",
			"module x\n\nrequire github.com/kitsunium/sdk/pkg v0.1.9\n" +
				"replace github.com/kitsunium/sdk/pkg => ../sdk/pkg\n",
			"v0.1.9", true, true,
		},
		{
			// The block form is the common one in a monorepo. Reading its lines
			// as requires turns "pkg => ../sdk/pkg" into a requirement on the
			// version "=>", which compares older than every real tag and yields
			// a confident, wrong "you are 25 releases behind".
			"replace block suppresses the check",
			"module x\n\nrequire (\n\tgithub.com/kitsunium/sdk/pkg v0.1.24\n)\n\n" +
				"replace (\n\tgithub.com/kitsunium/sdk/pkg => ../sdk/pkg\n)\n",
			"v0.1.24", true, true,
		},
		{
			// Direction matters: this mentions the SDK without replacing it.
			// Reading it as a replacement would silently suppress the warning
			// for a consumer who is genuinely behind.
			"SDK as the TARGET of a replace is not a replacement of it",
			"module x\n\nrequire github.com/kitsunium/sdk/pkg v0.1.9\n" +
				"replace example.com/fork => github.com/kitsunium/sdk/pkg v0.1.24\n",
			"v0.1.9", false, true,
		},
		{
			"require block with an indirect marker",
			"module x\n\nrequire (\n\tgithub.com/kitsunium/sdk/pkg v0.1.9 // indirect\n)\n",
			"v0.1.9", false, true,
		},
		{
			// A commented-out directive is not a directive.
			"commented directives are ignored",
			"module x\n\nrequire github.com/kitsunium/sdk/pkg v0.1.24\n" +
				"// replace github.com/kitsunium/sdk/pkg => ../x\n",
			"v0.1.24", false, true,
		},
		{
			// A malformed entry must not be read as a version; "=>" comparing
			// older than every tag is exactly how the bug above manifested.
			"a non-version second field is not a version",
			"module x\n\nrequire (\n\tgithub.com/kitsunium/sdk/pkg => ../x\n)\n",
			"", false, false,
		},
		{
			"no SDK requirement",
			"module x\n\nrequire example.com/other v1.0.0\n",
			"", false, false,
		},
		{
			// The internal modules are indirect requirements; only the public
			// module drives the advice.
			"indirect internal modules are not the public module",
			"module x\n\nrequire (\n\tgithub.com/kitsunium/sdk/internal/core v0.1.24 // indirect\n)\n",
			"", false, false,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		ref, ok := sdkRequirement(modDir(t, c.gomod))
		if ok != c.wantFound {
			t.Fatalf("found = %v, want %v", ok, c.wantFound)
		}
		if !ok {
			return
		}
		if ref.Version != c.wantVersion {
			t.Errorf("version = %q, want %q", ref.Version, c.wantVersion)
		}
		if ref.Replaced != c.wantReplace {
			t.Errorf("replaced = %v, want %v", ref.Replaced, c.wantReplace)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// go.mod is found by walking up, so the tool works from a package
// subdirectory the way every other Go tool does.
func TestGoModIsFoundFromASubdirectory(t *testing.T) {
	t.Parallel()
	root := modDir(t, "module x\n\nrequire github.com/kitsunium/sdk/pkg v0.1.1\n")
	deep := filepath.Join(root, "internal", "app")
	if err := os.MkdirAll(deep, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	ref, ok := sdkRequirement(deep)
	if !ok || ref.Version != "v0.1.1" {
		t.Errorf("walk-up failed: %+v ok=%v", ref, ok)
	}
}

// The end-to-end probe: outdated warns, current says nothing.
func TestCheckVersion(t *testing.T) {
	t.Parallel()
	type tc struct {
		name      string
		required  string
		published []string
		wantStale bool
		wantText  string
	}
	tests := []tc{
		{
			"behind by patches",
			"v0.1.9",
			[]string{"v0.1.9", "v0.1.10", "v0.1.24"},
			true, "2 patch releases behind",
		},
		{
			"on the latest release",
			"v0.1.24",
			[]string{"v0.1.9", "v0.1.24"},
			false, "",
		},
		{
			"ahead of the proxy (unreleased local tag)",
			"v0.2.0",
			[]string{"v0.1.24"},
			false, "",
		},
		{
			"a major gap says so",
			"v0.9.0",
			[]string{"v0.9.0", "v1.0.0"},
			true, "crossing a MAJOR version",
		},
		{
			"a minor gap says so",
			"v0.1.0",
			[]string{"v0.1.0", "v0.2.0"},
			true, "crossing a minor version",
		},
		{
			// Prereleases must never be recommended as the latest.
			"prereleases are not candidates",
			"v0.1.24",
			[]string{"v0.1.24", "v0.2.0-rc1", "v0.0.0-20260101120000-abcdef123456"},
			false, "",
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		dir := modDir(t, "module x\n\nrequire "+sdkModule+" "+c.required+"\n")
		notice, stale := checkVersion(dir, proxyStub(t, c.published...))
		if stale != c.wantStale {
			t.Fatalf("stale = %v, want %v (notice: %q)", stale, c.wantStale, notice)
		}
		if c.wantText != "" && !strings.Contains(notice, c.wantText) {
			t.Errorf("notice %q does not contain %q", notice, c.wantText)
		}
		if stale && !strings.Contains(notice, "go get "+sdkModule+"@") {
			t.Errorf("notice gives no upgrade command: %q", notice)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Every failure path must degrade to silence. A freshness nudge that breaks a
// build has failed at being a nudge.
func TestCheckVersionDegradesSilently(t *testing.T) {
	t.Parallel()
	stub := proxyStub(t, "v9.9.9")

	if _, stale := checkVersion(t.TempDir(), stub); stale {
		t.Error("warned with no go.mod present")
	}

	dir := modDir(t, "module x\n\nrequire "+sdkModule+" v0.1.0\n")
	dead := probe{proxy: "http://127.0.0.1:9", client: &http.Client{}}
	if _, stale := checkVersion(dir, dead); stale {
		t.Error("warned despite an unreachable proxy")
	}
}

// GOPROXY is read the way the go command reads it, including the fallback
// list — returning only the first entry made the documented fallback support a
// fiction.
func TestResolveProxiesHonoursGOPROXY(t *testing.T) {
	type tc struct {
		name    string
		env     string
		want    []string
		enabled bool
	}
	tests := []tc{
		{"unset falls back to the default", "", []string{defaultProxy}, true},
		{"off disables the probe", "off", nil, false},
		{"direct alone disables it", "direct", nil, false},
		{
			"a comma list keeps every URL in order",
			"https://a.example,https://b.example,direct",
			[]string{"https://a.example", "https://b.example"}, true,
		},
		{
			"a pipe list keeps every URL in order",
			"https://b.example|https://c.example",
			[]string{"https://b.example", "https://c.example"}, true,
		},
		{"trailing slash trimmed", "https://d.example/", []string{"https://d.example"}, true},
		{"off ahead of a URL still disables", "off,https://e.example", nil, false},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			// No t.Parallel: these mutate a process-wide environment variable.
			t.Setenv("GOPROXY", c.env)
			got, enabled := resolveProxies()
			if enabled != c.enabled || len(got) != len(c.want) {
				t.Fatalf("resolveProxies() = %v/%v, want %v/%v", got, enabled, c.want, c.enabled)
			}
			for i := range c.want {
				if got[i] != c.want[i] {
					t.Errorf("proxy[%d] = %q, want %q", i, got[i], c.want[i])
				}
			}
		})
	}
}

// A dead primary proxy must fall through to the backup rather than silently
// losing the freshness check.
func TestProxyFallbackIsAttempted(t *testing.T) {
	// No t.Parallel: t.Setenv mutates a process-wide variable.
	good := proxyStub(t, "v0.1.24")
	p := probe{client: good.client}
	t.Setenv("GOPROXY", "http://127.0.0.1:9,"+good.proxy)

	got, err := p.versions(sdkModule)
	if err != nil {
		t.Fatalf("versions: %v (fallback not attempted)", err)
	}
	if len(got) != 1 || got[0] != "v0.1.24" {
		t.Errorf("versions = %v, want [v0.1.24]", got)
	}
}

// A workspace root has no go.mod of its own, so the documented `sdkguard ./...`
// form from there would scan every module and warn about none of them. The
// OLDEST requirement wins: warning about the newest would let a stale module
// hide behind an up-to-date sibling.
func TestWorkspaceFreshnessUsesTheOldestModule(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	write("go.work", "go 1.24\n\nuse (\n\t./m1\n\t./m2\n)\n")
	write("m1/go.mod", "module m1\n\nrequire "+sdkModule+" v0.1.20\n")
	write("m2/go.mod", "module m2\n\nrequire "+sdkModule+" v0.1.9\n")

	notice, stale := checkVersion(root, proxyStub(t, "v0.1.9", "v0.1.20", "v0.1.24"))
	if !stale {
		t.Fatal("workspace freshness silently skipped")
	}
	if !strings.Contains(notice, "v0.1.9") {
		t.Errorf("notice names the wrong module; want the oldest (v0.1.9): %q", notice)
	}
}

// A replace anywhere in the workspace means someone is working locally.
func TestWorkspaceWithAReplaceStaysSilent(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "m1"), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for rel, content := range map[string]string{
		"go.work":   "go 1.24\n\nuse ./m1\n",
		"m1/go.mod": "module m1\n\nrequire " + sdkModule + " v0.1.9\nreplace " + sdkModule + " => ../sdk/pkg\n",
	} {
		if err := os.WriteFile(filepath.Join(root, rel), []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	if _, stale := checkVersion(root, proxyStub(t, "v0.1.24")); stale {
		t.Error("warned despite a workspace-local replace")
	}
}

// The mode flag is validated, and "off" must not even look for a go.mod.
func TestReportVersionValidatesItsMode(t *testing.T) {
	t.Parallel()
	if _, err := reportVersion(".", "bogus"); err == nil {
		t.Error("unknown mode accepted")
	}
	stale, err := reportVersion(".", versionCheckOff)
	if err != nil || stale {
		t.Errorf("off mode: stale=%v err=%v, want false/nil", stale, err)
	}
}
