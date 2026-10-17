package kit_test

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A product that imports framework/kit alone initialises no subsystem it did
// not import (ADR 0172): neither the HTTP engine and the frontends' file server
// (framework/kit/server), nor the Studio's event stream and profiler
// (framework/kit/studio), nor the process sampler — runtime/metrics, whose
// initialisation every process importing it pays, and the spawn engine behind
// pkg/v1/proc/process. Go runs the initialisation of every package a binary
// imports, used or not, so the import graph is what decides.
//
// It lists the graph with the go command, and skips without one or under
// Bazel, whose sandbox holds no module. Its gate is CI's test-386 job, which
// runs go test over every module with the toolchain on PATH (rule 12).
func TestTheFacadeImportsNoOptInSubsystem(t *testing.T) {
	goTool := goToList(t)
	deps := listDeps(t, goTool, "github.com/kitsunium/sdk/framework/kit")
	if !deps["github.com/kitsunium/sdk/framework/internal/kit"] {
		t.Fatalf("the listing does not hold framework/internal/kit: %v", deps)
	}
	for _, opt := range []struct{ pkg, by string }{
		{"github.com/kitsunium/sdk/pkg/v1/net/server", "framework/kit/server"},
		{"github.com/kitsunium/sdk/pkg/v1/net/static", "framework/kit/server"},
		{"github.com/kitsunium/sdk/pkg/v1/proc/process", "framework/kit/server"},
		{"github.com/kitsunium/sdk/internal/service/proc/exec", "framework/kit/server"},
		{"runtime/metrics", "framework/kit/server"},
		{"github.com/kitsunium/sdk/pkg/v1/net/sse", "framework/kit/studio"},
		{"github.com/kitsunium/sdk/pkg/v1/observe/profiling", "framework/kit/studio"},
	} {
		if deps[opt.pkg] {
			t.Errorf("framework/kit imports %s, which only %s may bring", opt.pkg, opt.by)
		}
	}
}

// sdk is the repository's module path, which every package below is under.
const sdk = "github.com/kitsunium/sdk/"

// leaving are the subsystems ADR 0179 §1 takes out of framework/internal/kit,
// each by the SDK package its engine lives in: the registration package that
// brings it once it has left, the migration step that moves it, and the
// packages whose machine code is the subsystem's in a binary — the SDK
// package's core and service halves, whose names its symbols carry; kit's
// own store engines for the stores.
var leaving = []leavingCase{
	{"pkg/v1/data/docstore", "framework/kit/store", 3, []string{"internal/core/data/docstore.", "internal/service/data/docstore."}},
	{"pkg/v1/data/sql", "framework/kit/store", 3, []string{"internal/core/data/sql.", "internal/service/data/sql."}},
	{"pkg/v1/data/queue", "framework/kit/queue", 4, []string{"internal/core/data/queue.", "internal/service/data/queue."}},
	{"pkg/v1/app/statemachine", "framework/kit/workflow", 5, []string{"internal/core/app/statemachine.", "internal/service/app/statemachine."}},
	{"pkg/v1/app/mail/spool", "framework/kit/mail", 6, []string{"internal/core/app/mail/spool.", "internal/service/app/mail/spool."}},
	{"pkg/v1/app/scheduler", "framework/kit/jobs", 7, []string{"internal/core/app/scheduler.", "internal/service/app/scheduler."}},
	{"pkg/v1/app/i18n", "framework/kit/studio", 8, []string{"internal/core/app/i18n.", "internal/service/app/i18n."}},
}

// leavingCase is one subsystem of leaving: the SDK package its engine lives
// in, its registration package, its step, and the prefixes of its symbols.
type leavingCase struct {
	pkg, by string
	step    int
	code    []string
}

// storeEngines are the symbols of kit's store engines instantiated per
// record type, in a binary: none since ADR 0179's step 2 made them one
// non-generic engine over documents — and the agendas of the retention and
// the trash with them —, which step 3 moves to framework/internal/kit/storekit.
// StoreService[T] is not among them: it is the typed facade that stays
// generic (ADR 0179 §2), a few lines per method, linked for kit's own
// stores and each a product declares.
var storeEngines = []string{
	"framework/internal/kit.(*docEngine[", "framework/internal/kit.(*sqlEngine[",
	"framework/internal/kit.(*sealedEngine[", "framework/internal/kit.(*historied[",
	"framework/internal/kit.(*retentionRun[", "framework/internal/kit.(*trashRun[",
}

// stillInCore is the allow-list while ADR 0179's migration runs: the SDK
// packages of leaving that framework/internal/kit still imports. Each step
// takes its own entries out in the same change that moves them — step 2 took
// kit's generic store engines ("store engines"), step 3 docstore and sql,
// step 4 queue, step 5 statemachine, step 6 mail/spool, step 7 scheduler,
// step 8 i18n —, and step 10, which drops the facade's blank imports,
// deletes it with this test's allowances. It is held tight both ways: a
// package still listed that the core no longer imports fails the test as
// surely as one the core imports and the list does not name.
var stillInCore = map[string]bool{
	"pkg/v1/data/docstore":    true,
	"pkg/v1/data/sql":         true,
	"pkg/v1/data/queue":       true,
	"pkg/v1/app/statemachine": true,
	"pkg/v1/app/mail/spool":   true,
	"pkg/v1/app/scheduler":    true,
	"pkg/v1/app/i18n":         true,
}

// The core imports none of the subsystems ADR 0179 §1 takes out of it, but
// those the allow-list still names; the facade brings one only through its
// registration package, which it blank-imports until step 10; and plug, the
// contract between the core and every subsystem, imports the standard
// library and the framework's model only (ADR 0179 §3, §7).
func TestTheCoreImportsOnlyTheSubsystemsStillInIt(t *testing.T) {
	goTool := goToList(t)
	core := listDeps(t, goTool, sdk+"framework/internal/kit")
	facade := listDeps(t, goTool, sdk+"framework/kit")
	runCase := func(t *testing.T, l leavingCase) {
		t.Helper()
		inCore := core[sdk+l.pkg]
		switch {
		case inCore && !stillInCore[l.pkg]:
			t.Errorf("framework/internal/kit imports %s, which only %s may bring once step %d of ADR 0179 moved it", l.pkg, l.by, l.step)
		case !inCore && stillInCore[l.pkg]:
			t.Errorf("framework/internal/kit no longer imports %s: take it off stillInCore", l.pkg)
		case facade[sdk+l.pkg] && !inCore && !facade[sdk+l.by]:
			t.Errorf("framework/kit brings %s without %s", l.pkg, l.by)
		}
	}
	for _, l := range leaving {
		t.Run(l.pkg, func(t *testing.T) { runCase(t, l) })
	}
	for p := range goList(t, goTool, sdk+"framework/internal/kit/plug", "-f", "{{join .Imports \" \"}}") {
		if strings.Contains(strings.Split(p, "/")[0], ".") && p != sdk+"framework/model" {
			t.Errorf("plug imports %s: it imports the standard library and framework/model only", p)
		}
	}
}

// A product that imports no registration package links none of the code of
// the subsystems ADR 0179 §1 takes out of the core, nor kit's store engines,
// but what the allow-list still names: an import graph says what is
// initialised, the symbol table what is linked. It builds testdata/cliprobe,
// the smallest product, and reads its text symbols with go tool nm. The other
// half of §7 — a program that imports a registration package does link them
// — is written with the first registration package, framework/kit/store
// (step 3): before it there is nothing to import.
func TestTheSmallestProductLinksOnlyTheSubsystemsStillInTheCore(t *testing.T) {
	goTool := goToList(t)
	bin := filepath.Join(t.TempDir(), "cliprobe")
	build := exec.Command(goTool, "build", "-o", bin, "./testdata/cliprobe")
	build.Env = append(os.Environ(), "GOWORK=off")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building the probe: %v\n%s", err, out)
	}
	nm := exec.Command(goTool, "tool", "nm", "-type", bin)
	out, err := nm.Output()
	if err != nil {
		t.Fatalf("go tool nm: %v", err)
	}
	linked := map[string]int{}
	owners := map[string][]string{"store engines": storeEngines}
	for _, l := range leaving {
		owners[l.pkg] = l.code
	}
	lines := bufio.NewScanner(bytes.NewReader(out))
	lines.Buffer(make([]byte, 0, 64*1024), 4<<20)
	for lines.Scan() {
		f := strings.Fields(lines.Text())
		// address, symbol type, name, Go type: a text symbol is T or t.
		if len(f) < 3 || (f[1] != "T" && f[1] != "t") {
			continue
		}
		for owner, prefixes := range owners {
			for _, p := range prefixes {
				if strings.HasPrefix(f[2], sdk+p) {
					linked[owner]++
				}
			}
		}
	}
	if err := lines.Err(); err != nil {
		t.Fatal(err)
	}
	runCase := func(t *testing.T, owner string) {
		t.Helper()
		n := linked[owner]
		if n == 0 {
			return
		}
		if !stillInCore[owner] {
			t.Errorf("the smallest product links %d text symbols of %s, which only its registration package may bring", n, owner)
			return
		}
		t.Logf("still in the core (ADR 0179 allow-list): %s, %d text symbols linked", owner, n)
	}
	for owner := range owners {
		t.Run(owner, func(t *testing.T) { runCase(t, owner) })
	}
}

// goToList is the go command the graph is listed and the probe built with;
// the test skips without one, and under Bazel, whose sandbox holds the
// test's runfiles and no module even where the runner put a toolchain on
// PATH.
func goToList(t *testing.T) string {
	t.Helper()
	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go toolchain to list the import graph")
	}
	if os.Getenv("TEST_SRCDIR") != "" {
		t.Skip("under Bazel: no module to list")
	}
	return goTool
}

// listDeps is go list -deps of pkg, outside the workspace, as a set.
func listDeps(t *testing.T, goTool, pkg string) map[string]bool {
	t.Helper()
	return goList(t, goTool, pkg, "-deps")
}

// goList is what go list prints of pkg given args, outside the workspace, as
// a set of words.
func goList(t *testing.T, goTool, pkg string, args ...string) map[string]bool {
	t.Helper()
	list := exec.Command(goTool, append(append([]string{"list"}, args...), pkg)...)
	list.Env = append(os.Environ(), "GOWORK=off")
	out, err := list.Output()
	if err != nil {
		t.Fatalf("go list -deps %s: %v", pkg, err)
	}
	deps := make(map[string]bool)
	for p := range strings.FieldsSeq(string(out)) {
		deps[p] = true
	}
	return deps
}
