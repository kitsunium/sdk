package kit_test

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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
// package's core and service halves, whose names its symbols carry.
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

// kitEngines are the implementation packages of the subsystems that have
// left the core, by the registration package that alone imports each: their
// symbols in a binary are the subsystem's (ADR 0179 §1, step 3 on).
var kitEngines = map[string]string{
	"framework/kit/store": "framework/internal/kit/storekit.",
}

// stillInCore is the allow-list while ADR 0179's migration runs: the SDK
// packages of leaving that framework/internal/kit still imports. Each step
// takes its own entries out in the same change that moves them — step 3 took
// docstore and sql, step 4 takes queue, step 5 statemachine, step 6
// mail/spool, step 7 scheduler, step 8 i18n —, and step 10, which drops the
// facade's blank imports, deletes it with this test's allowances. It is held
// tight both ways: a package still listed that the core no longer imports
// fails the test as surely as one the core imports and the list does not
// name.
var stillInCore = map[string]bool{
	"pkg/v1/data/queue":       true,
	"pkg/v1/app/statemachine": true,
	"pkg/v1/app/mail/spool":   true,
	"pkg/v1/app/scheduler":    true,
	"pkg/v1/app/i18n":         true,
}

// throughStill are the packages of stillInCore whose own graph holds pkg:
// what the core reaches pkg through, once it imports pkg no more itself —
// the SDK's sql under its queue's SQL broker and its mail spool, until steps
// 4 and 6 take those out. A package the core reaches through none of them is
// the core's own import.
func throughStill(t *testing.T, goTool, pkg string) []string {
	t.Helper()
	var via []string
	for p := range stillInCore {
		if listDeps(t, goTool, sdk+p)[sdk+pkg] {
			via = append(via, p)
		}
	}
	slices.Sort(via)
	return via
}

// The core imports none of the subsystems ADR 0179 §1 takes out of it, but
// those the allow-list still names — a package it reaches only through one
// of those is theirs, until their step —; the facade brings one only through
// its registration package, which it blank-imports until step 10; and plug,
// the contract between the core and every subsystem, imports the standard
// library and the framework's model only (ADR 0179 §3, §7).
func TestTheCoreImportsOnlyTheSubsystemsStillInIt(t *testing.T) {
	goTool := goToList(t)
	core := listDeps(t, goTool, sdk+"framework/internal/kit")
	direct := goList(t, goTool, sdk+"framework/internal/kit", "-f", "{{join .Imports \" \"}}")
	facade := listDeps(t, goTool, sdk+"framework/kit")
	runCase := func(t *testing.T, l leavingCase) {
		t.Helper()
		inCore := core[sdk+l.pkg]
		switch {
		case direct[sdk+l.pkg] && !stillInCore[l.pkg]:
			t.Errorf("framework/internal/kit imports %s, which only %s may bring once step %d of ADR 0179 moved it", l.pkg, l.by, l.step)
		case inCore && !stillInCore[l.pkg]:
			via := throughStill(t, goTool, l.pkg)
			if len(via) == 0 {
				t.Errorf("framework/internal/kit reaches %s through none of the subsystems still in it: only %s may bring it once step %d of ADR 0179 moved it", l.pkg, l.by, l.step)
				return
			}
			t.Logf("framework/internal/kit no longer imports %s, and reaches it through %s, still in it (ADR 0179 allow-list)", l.pkg, strings.Join(via, ", "))
		case !inCore && stillInCore[l.pkg]:
			t.Errorf("framework/internal/kit no longer imports %s: take it off stillInCore", l.pkg)
		case facade[sdk+l.pkg] && !inCore && !facade[sdk+l.by]:
			t.Errorf("framework/kit brings %s without %s", l.pkg, l.by)
		}
	}
	for _, l := range leaving {
		t.Run(l.pkg, func(t *testing.T) { runCase(t, l) })
	}
	for by, engine := range kitEngines {
		pkg := sdk + strings.TrimSuffix(engine, ".")
		if core[pkg] {
			t.Errorf("framework/internal/kit imports %s, which only %s may bring", pkg, by)
		}
		if !facade[sdk+by] {
			t.Errorf("framework/kit does not import %s, which it blank-imports until ADR 0179's step 10", by)
		}
	}
	for p := range goList(t, goTool, sdk+"framework/internal/kit/plug", "-f", "{{join .Imports \" \"}}") {
		if strings.Contains(strings.Split(p, "/")[0], ".") && p != sdk+"framework/model" {
			t.Errorf("plug imports %s: it imports the standard library and framework/model only", p)
		}
	}
}

// textSymbols builds the program at dir and lists its text symbols: an
// import graph says what is initialised, the symbol table what is linked.
func textSymbols(t *testing.T, goTool, dir string) map[string]bool {
	t.Helper()
	bin := filepath.Join(t.TempDir(), filepath.Base(dir))
	build := exec.Command(goTool, "build", "-o", bin, dir)
	build.Env = append(os.Environ(), "GOWORK=off")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building %s: %v\n%s", dir, err, out)
	}
	nm := exec.Command(goTool, "tool", "nm", "-type", bin)
	out, err := nm.Output()
	if err != nil {
		t.Fatalf("go tool nm: %v", err)
	}
	syms := map[string]bool{}
	lines := bufio.NewScanner(bytes.NewReader(out))
	lines.Buffer(make([]byte, 0, 64*1024), 4<<20)
	for lines.Scan() {
		f := strings.Fields(lines.Text())
		// address, symbol type, name, Go type: a text symbol is T or t.
		if len(f) >= 3 && (f[1] == "T" || f[1] == "t") {
			syms[f[2]] = true
		}
	}
	if err := lines.Err(); err != nil {
		t.Fatal(err)
	}
	return syms
}

// linkedSymbols counts syms by owner: the text symbols under each owner's
// prefixes.
func linkedSymbols(syms map[string]bool, owners map[string][]string) map[string]int {
	linked := map[string]int{}
	for s := range syms {
		for owner, prefixes := range owners {
			for _, p := range prefixes {
				if strings.HasPrefix(s, sdk+p) {
					linked[owner]++
				}
			}
		}
	}
	return linked
}

// kitStoreCode is kit's own store code, which stays in the core because it
// names no SDK type (ADR 0179 §3) and which a program reaches only through
// framework/kit/store (EnableStores, step 3b): one function of each part, by
// its symbol — the privacy service kit mounts and its journal, sealing,
// the stores' engines over documents and their history, the databases, their
// migrations and checks, the retention, the trash, and the commands on the
// stores. A product that does not import the package links
// none of them; one that does links every one, so that the list cannot go
// stale by naming what nothing links.
var kitStoreCode = []string{
	"(*App).mountPrivacyService",
	"(*App).appendToJournal",
	"(*App).sealing",
	"(*App).sealingProblems",
	"(*App).indexKeyProblems",
	"(*App).privacyProblems",
	"(*storeCore).start",
	"(*sealedEngine).Write",
	"(*historied).Write",
	"(*App).addDatabaseComponents",
	"(*App).startDatabase",
	"(*App).databaseProblems",
	"(*App).migrateAtStart",
	"(*App).addRetentionComponent",
	"(*App).addTrashComponent",
	"(*App).migrateCommand",
	"(*App).privacyCommand",
	"(*App).revisionsCommand",
	"(*App).dataCommand",
	"(*App).watchCommand",
}

// kitStoreSymbol is the text symbol of one entry of kitStoreCode.
func kitStoreSymbol(fn string) string { return sdk + "framework/internal/kit." + fn }

// owners are the symbol prefixes of each subsystem of leaving, and of each
// implementation package that left the core, by its registration package.
func owners() map[string][]string {
	out := map[string][]string{}
	for _, l := range leaving {
		out[l.pkg] = l.code
	}
	for by, engine := range kitEngines {
		out[by] = []string{engine}
	}
	return out
}

// A product that imports no registration package links none of the code of
// the subsystems ADR 0179 §1 takes out of the core, nor the engines of those
// that left it, but what the allow-list still names — and what a subsystem
// still in the core reaches, the SDK's sql under the queues' SQL broker. It
// builds testdata/coreprobe, the smallest product on the core alone: the
// facade imports every registration package until step 10, which a product
// built on it links.
func TestTheSmallestProductLinksOnlyTheSubsystemsStillInTheCore(t *testing.T) {
	goTool := goToList(t)
	syms := textSymbols(t, goTool, "./testdata/coreprobe")
	linked := linkedSymbols(syms, owners())
	for _, fn := range kitStoreCode {
		if syms[kitStoreSymbol(fn)] {
			t.Errorf("a product that does not import framework/kit/store links kit's own store code: %s", fn)
		}
	}
	runCase := func(t *testing.T, owner string) {
		t.Helper()
		n := linked[owner]
		switch {
		case n == 0:
			return
		case stillInCore[owner]:
			t.Logf("still in the core (ADR 0179 allow-list): %s, %d text symbols linked", owner, n)
		case kitEngines[owner] != "":
			t.Errorf("a product that does not import %s links %d text symbols of its engines", owner, n)
		default:
			via := throughStill(t, goTool, owner)
			if len(via) == 0 {
				t.Errorf("the smallest product links %d text symbols of %s, which only its registration package may bring", n, owner)
				return
			}
			t.Logf("%s: %d text symbols linked through %s, still in the core (ADR 0179 allow-list)", owner, n, strings.Join(via, ", "))
		}
	}
	for owner := range owners() {
		t.Run(owner, func(t *testing.T) { runCase(t, owner) })
	}
}

// A product that imports a registration package links the code it brings —
// the other half of §7, so that the half above cannot pass by testing
// nothing: testdata/storeprobe, coreprobe with framework/kit/store, links the
// store subsystem's engines, the SDK's document store and sql under them,
// and kit's own store code.
func TestAProductThatImportsTheStoresLinksThem(t *testing.T) {
	goTool := goToList(t)
	syms := textSymbols(t, goTool, "./testdata/storeprobe")
	linked := linkedSymbols(syms, owners())
	for _, owner := range []string{"framework/kit/store", "pkg/v1/data/docstore", "pkg/v1/data/sql"} {
		if linked[owner] == 0 {
			t.Errorf("a product that imports framework/kit/store links no text symbol of %s", owner)
		}
	}
	for _, fn := range kitStoreCode {
		if !syms[kitStoreSymbol(fn)] {
			t.Errorf("a product that imports framework/kit/store does not link kit's own store code: %s", fn)
		}
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
