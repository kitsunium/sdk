package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// write drops src into a fresh directory and returns that directory.
func write(t *testing.T, name, src string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return dir
}

// idsOf renders the findings as "RULE" strings for compact assertions.
func idsOf(findings []Finding) []string {
	out := make([]string, 0, len(findings))
	for _, f := range findings {
		out = append(out, f.Rule)
	}
	return out
}

// scan runs every rule over one fixture file.
func scan(t *testing.T, src string) []Finding {
	t.Helper()
	dir := write(t, "fixture.go", src)
	found, err := scanDir(dir, allRules, false)
	if err != nil {
		t.Fatalf("scanDir: %v", err)
	}
	return found
}

// Each rule must fire on the construct it names. A rule that never fires is
// indistinguishable from a rule that does not exist.
func TestRulesFireOnViolations(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		src  string
		want string
	}
	tests := []tc{
		{
			"SDK001 slog handler constructor",
			"package p\nimport (\"log/slog\"; \"os\")\nvar _ = slog.NewTextHandler(os.Stderr, nil)\n",
			"SDK001",
		},
		{
			"SDK001 slog.SetDefault",
			"package p\nimport \"log/slog\"\nfunc f(l *slog.Logger) { slog.SetDefault(l) }\n",
			"SDK001",
		},
		{
			"SDK002 fmt.Errorf",
			"package p\nimport \"fmt\"\nvar _ = fmt.Errorf(\"boom\")\n",
			"SDK002",
		},
		{
			"SDK002 errors.New",
			"package p\nimport \"errors\"\nvar _ = errors.New(\"boom\")\n",
			"SDK002",
		},
		{
			"SDK003 stdout as a logger Config Writer",
			"package p\nimport (\"os\"\n\"github.com/kitsunium/sdk/pkg/v1/logger\")\n" +
				"var _ = logger.Config{Writer: os.Stdout}\n",
			"SDK003",
		},
		{
			"SDK003 stdout inside a Writers fan-out",
			"package p\nimport (\"io\"\n\"os\"\n\"github.com/kitsunium/sdk/pkg/v1/logger\")\n" +
				"var _ = logger.Config{Writers: []io.Writer{os.Stderr, os.Stdout}}\n",
			"SDK003",
		},
		{
			// A dot import binds no qualifier, so no selector rule can see
			// through it. Reporting the blind spot beats passing quietly.
			"SDK001 reports a dot-imported slog as unanalysable",
			"package p\nimport (\n. \"log/slog\"\n\"os\"\n)\nvar _ = New(NewTextHandler(os.Stderr, nil))\n",
			"SDK001",
		},
		{
			"SDK004 runtime assignment to logger.Version",
			"package p\nimport \"github.com/kitsunium/sdk/pkg/v1/logger\"\n" +
				"func f() { logger.Version = \"v1\" }\n",
			"SDK004",
		},
		{
			"SDK005 legacy log package",
			"package p\nimport \"log\"\nfunc f() { log.Printf(\"x\") }\n",
			"SDK005",
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := idsOf(scan(t, c.src))
		for _, id := range got {
			if id == c.want {
				return
			}
		}
		t.Errorf("rule %s did not fire; got %v", c.want, got)
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// The rules must stay silent on the code they were carefully scoped around.
// A false positive is more expensive than a missed finding: it is what turns a
// tool off for good.
func TestRulesStaySilentOnLegitimateCode(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		src  string
	}
	tests := []tc{
		{
			// The bridge's whole point: the *slog.Logger type and the attr
			// constructors are vocabulary a consumer legitimately needs.
			"slog type and attrs without building a pipeline",
			"package p\nimport \"log/slog\"\ntype S struct{ L *slog.Logger }\n" +
				"var _ = slog.String(\"k\", \"v\")\n",
		},
		{
			// 5gc-mcp's `run` mode prints results to stdout on purpose. ADR 0030
			// is about stdout carrying a protocol, not about stdout being banned.
			"stdout as plain CLI output",
			"package p\nimport (\"encoding/json\"; \"fmt\"; \"os\")\n" +
				"func f() { fmt.Fprintln(os.Stdout, \"x\"); _ = json.NewEncoder(os.Stdout) }\n",
		},
		{
			// A selector on a same-named local in a file that does not import
			// the package is not a match.
			"local variable shadowing a package name",
			"package p\ntype t struct{ Errorf func(string) error }\n" +
				"func f(fmt t) { _ = fmt.Errorf(\"x\") }\n",
		},
		{
			"stderr as a Writer field is the correct destination",
			"package p\nimport (\"os\"\n\"github.com/kitsunium/sdk/pkg/v1/logger\")\n" +
				"var _ = logger.Config{Writer: os.Stderr}\n",
		},
		{
			// The field name alone concludes nothing: a Report writing its
			// result to stdout is a CLI doing its job, which ADR 0030 permits.
			// Only a logging package's struct makes stdout a log destination.
			"an Output field on a non-logging struct",
			"package p\nimport (\"io\"\n\"os\"\n)\ntype Report struct{ Output io.Writer }\n" +
				"var _ = Report{Output: os.Stdout}\n",
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := scan(t, c.src); len(got) != 0 {
			t.Errorf("false positive: %v", got)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// The sanctioned composition must not fail the invariant it steers people
// towards. NewHandler returns (handler, error), so real code binds it first —
// missing that form would leave the rule firing on the documented answer.
func TestSanctionedBridgeCompositionIsNotAPipeline(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		src  string
		want int
	}
	const imports = "package p\nimport (\n\"log/slog\"\n" +
		"\"github.com/kitsunium/sdk/pkg/v1/logger\"\n" +
		"\"github.com/kitsunium/sdk/pkg/v1/logger/slogbridge\"\n)\n"
	tests := []tc{
		{
			"handler bound to a variable",
			imports + "func f(lg logger.Logger) *slog.Logger {\n" +
				"h, _ := slogbridge.NewHandler(lg)\nreturn slog.New(h)\n}\n",
			0,
		},
		{
			"handler inlined",
			imports + "func f(h slog.Handler) *slog.Logger { return slog.New(h) }\n" +
				"func g(lg logger.Logger) (slog.Handler, error) { return slogbridge.NewHandler(lg) }\n",
			1, // slog.New(h) here wraps an unrelated handler, so it still counts
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := scan(t, c.src)
		if len(got) != c.want {
			t.Errorf("findings = %d, want %d (%v)", len(got), c.want, idsOf(got))
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// A file no build includes, or one a generator owns, is not the consumer's to
// fix — reporting it is noise they cannot action.
func TestFilesOutsideTheConsumersControlAreSkipped(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		src  string
		want int
	}
	tests := []tc{
		{
			"build-ignored file",
			"//go:build ignore\n\npackage p\nimport \"log\"\nfunc f() { log.Print(\"x\") }\n",
			0,
		},
		{
			"generated file",
			"// Code generated by stringer. DO NOT EDIT.\n\npackage p\n" +
				"import \"fmt\"\nvar _ = fmt.Errorf(\"x\")\n",
			0,
		},
		{
			// A platform-specific file IS part of a build; its rules apply on
			// the platform it targets, so it must still be scanned.
			"platform-constrained file is still analysed",
			"//go:build linux\n\npackage p\nimport \"log\"\nfunc f() { log.Print(\"x\") }\n",
			1,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := scan(t, c.src)
		if len(got) != c.want {
			t.Errorf("findings = %d, want %d (%v)", len(got), c.want, idsOf(got))
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// normalizeRoot must not turn a path into the working directory. Trimming the
// trailing separator blindly made "sdkguard /" scan the cwd instead of the
// filesystem root — a silent, wrong answer.
func TestNormalizeRoot(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   string
		want string
	}
	tests := []tc{
		{"empty means here", "", "."},
		{"ellipsis alone means here", "...", "."},
		{"dot-slash-ellipsis", "./...", "."},
		{"filesystem root survives", "/", "/"},
		{"recursive filesystem root survives", "/...", "/"},
		{"relative dir loses the trailing separator", "foo/...", "foo"},
		{"absolute dir", "/tmp/x/...", "/tmp/x"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := normalizeRoot(c.in); got != c.want {
			t.Errorf("normalizeRoot(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// A local that shadows an imported package must not produce findings. Without
// type resolution `logger.Version = x` on a local struct is indistinguishable
// from a write to the SDK's package variable, and `logger` is a name consumers
// bind constantly — matching anyway would report a violation that is not there.
func TestShadowedPackageNamesDoNotFire(t *testing.T) {
	t.Parallel()
	shadowed := "package p\n\nimport \"github.com/kitsunium/sdk/pkg/v1/logger\"\n\n" +
		"type myLog struct{ Version string }\n\n" +
		"func f() {\n\tlogger := myLog{}\n\tlogger.Version = \"1.0\"\n\t_ = logger\n}\n"
	if got := scan(t, shadowed); len(got) != 0 {
		t.Errorf("false positive on a shadowed package: %v", idsOf(got))
	}

	// The control: without shadowing the same write IS the violation.
	plain := "package p\n\nimport \"github.com/kitsunium/sdk/pkg/v1/logger\"\n\n" +
		"func f() { logger.Version = \"1.0\" }\n"
	got := scan(t, plain)
	if len(got) != 1 || got[0].Rule != "SDK004" {
		t.Errorf("real violation missed; got %v", idsOf(got))
	}
}

// An aliased import must be caught exactly like a plain one, or the rule is
// one rename away from silence.
func TestAliasedImportIsResolved(t *testing.T) {
	t.Parallel()
	src := "package p\nimport sl \"log/slog\"\nimport \"os\"\nvar _ = sl.NewJSONHandler(os.Stderr, nil)\n"
	if got := idsOf(scan(t, src)); len(got) == 0 || got[0] != "SDK001" {
		t.Errorf("aliased import missed; got %v", got)
	}
}

// A suppression carries a reason, and a bare directive does not suppress:
// an exemption nobody had to justify is the kind that outlives its reason.
func TestSuppressionRequiresAReason(t *testing.T) {
	t.Parallel()
	type tc struct {
		name      string
		directive string
		wantCount int
	}
	tests := []tc{
		{"reason given suppresses", "//sdkguard:allow SDK002 third-party contract needs a bare error", 0},
		{"bare directive does not suppress", "//sdkguard:allow SDK002", 1},
		{"wrong rule id does not suppress", "//sdkguard:allow SDK001 unrelated", 1},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		src := "package p\nimport \"fmt\"\nvar _ = fmt.Errorf(\"boom\") " + c.directive + "\n"
		if got := scan(t, src); len(got) != c.wantCount {
			t.Errorf("findings = %d, want %d (%v)", len(got), c.wantCount, idsOf(got))
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// A directive on the line above the finding suppresses it too, matching how
// //nolint and //go:build directives are already written.
func TestSuppressionOnPrecedingLine(t *testing.T) {
	t.Parallel()
	src := "package p\nimport \"fmt\"\n" +
		"//sdkguard:allow SDK002 documented exemption\nvar _ = fmt.Errorf(\"boom\")\n"
	if got := scan(t, src); len(got) != 0 {
		t.Errorf("preceding-line directive ignored: %v", idsOf(got))
	}
}

// Test files are excluded by default — fixtures legitimately mint throwaway
// errors — and included on demand.
func TestTestFilesAreOptIn(t *testing.T) {
	t.Parallel()
	dir := write(t, "thing_test.go", "package p\nimport \"errors\"\nvar _ = errors.New(\"x\")\n")

	off, err := scanDir(dir, allRules, false)
	if err != nil {
		t.Fatalf("scanDir: %v", err)
	}
	if len(off) != 0 {
		t.Errorf("test file scanned by default: %v", idsOf(off))
	}

	on, err := scanDir(dir, allRules, true)
	if err != nil {
		t.Fatalf("scanDir: %v", err)
	}
	if len(on) != 1 {
		t.Errorf("findings with -tests = %d, want 1", len(on))
	}
}

// Level filtering is what makes incremental adoption possible: a team runs the
// invariants first and turns conventions on when ready, instead of switching
// the whole tool off.
func TestLevelFiltering(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		level string
		want  int
	}
	tests := []tc{
		{"all rules", "", len(allRules)},
		{"invariants only", LevelInvariant, 3},
		{"conventions only", LevelConvention, 2},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got, err := filterLevel(allRules, c.level)
		if err != nil {
			t.Fatalf("filterLevel: %v", err)
		}
		if len(got) != c.want {
			t.Errorf("rules = %d, want %d", len(got), c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
	if _, err := filterLevel(allRules, "nonsense"); err == nil {
		t.Error("unknown level accepted")
	}
}

// Every rule must carry a level and an ADR reference, so a finding can always
// be traced back to the decision that motivated it.
func TestEveryRuleIsAttributed(t *testing.T) {
	t.Parallel()
	for _, r := range allRules {
		if r.Level != LevelInvariant && r.Level != LevelConvention {
			t.Errorf("%s: level = %q", r.ID, r.Level)
		}
		if !strings.Contains(r.Source, "ADR") && !strings.Contains(r.Source, "CLAUDE.md") {
			t.Errorf("%s: source %q names no decision record", r.ID, r.Source)
		}
	}
}

// selectRules must reject an unknown ID rather than silently running nothing.
func TestSelectRulesRejectsUnknownID(t *testing.T) {
	t.Parallel()
	if _, err := selectRules("SDK999"); err == nil {
		t.Error("unknown rule accepted")
	}
	got, err := selectRules("sdk001")
	if err != nil || len(got) != 1 || got[0].ID != "SDK001" {
		t.Errorf("case-insensitive selection failed: %v / %v", got, err)
	}
}

// scanRoots must accept the `./...` spelling developers already type, so the
// tool drops into an existing CI line without a new argument convention — and
// must order findings by file then line, so a diff of two runs is readable.
func TestScanRootsAcceptsEllipsisAndSortsOutput(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	nested := filepath.Join(dir, "sub")
	if err := os.MkdirAll(nested, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// b.go sorts after a.go; its finding must be reported second even though
	// WalkDir order is not guaranteed to be the order we want.
	if err := os.WriteFile(filepath.Join(dir, "b.go"),
		[]byte("package p\nimport \"log\"\nfunc f() { log.Print(\"x\") }\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nested, "a.go"),
		[]byte("package q\nimport \"fmt\"\nvar _ = fmt.Errorf(\"x\")\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := scanRoots([]string{dir + "/..."}, allRules, false)
	if err != nil {
		t.Fatalf("scanRoots: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("findings = %d, want 2 (%v)", len(got), idsOf(got))
	}
	if !strings.HasSuffix(got[0].Pos.Filename, "b.go") {
		t.Errorf("first finding = %s, want b.go (sorted before sub/a.go)", got[0].Pos.Filename)
	}
	if got[1].Rule != "SDK002" {
		t.Errorf("second finding = %s, want SDK002", got[1].Rule)
	}
}

// An unreadable root is a tool error the caller must see, not an empty result
// that would read as "no violations".
func TestScanRootsSurfacesAWalkError(t *testing.T) {
	t.Parallel()
	if _, err := scanRoots([]string{filepath.Join(t.TempDir(), "absent")}, allRules, false); err == nil {
		t.Error("missing root accepted silently")
	}
}
