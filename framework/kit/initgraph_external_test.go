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
	"framework/kit/store":    "framework/internal/kit/storekit.",
	"framework/kit/queue":    "framework/internal/kit/queuekit.",
	"framework/kit/workflow": "framework/internal/kit/workflowkit.",
	"framework/kit/mail":     "framework/internal/kit/mailkit.",
	"framework/kit/jobs":     "framework/internal/kit/jobkit.",
}

// stillInCore is the allow-list while ADR 0179's migration runs: the SDK
// packages of leaving that framework/internal/kit still imports. Each step
// takes its own entries out in the same change that moves them — step 3 took
// docstore and sql, step 4 queue, step 5 statemachine, step 6 mail/spool,
// step 7 scheduler, step 8 takes i18n —, and step 10, which drops the
// facade's blank imports, deletes it with this test's allowances. It is held
// tight both ways: a package still listed that the core no longer imports
// fails the test as surely as one the core imports and the list does not
// name.
var stillInCore = map[string]bool{
	"pkg/v1/app/i18n": true,
}

// throughStill are the packages of stillInCore whose own graph holds pkg, or
// the core or service half of the SDK package pkg names (leaving's code):
// what the core reaches pkg through, once it imports pkg no more itself —
// the SDK's sql and the queue's two halves were under the mail spool until
// step 6 took it out, and none is now. A package the core reaches through
// none of them is the core's own import.
func throughStill(t *testing.T, goTool, pkg string) []string {
	t.Helper()
	halves := []string{sdk + pkg}
	for _, l := range leaving {
		if l.pkg == pkg {
			for _, c := range l.code {
				halves = append(halves, sdk+strings.TrimSuffix(c, "."))
			}
		}
	}
	var via []string
	for p := range stillInCore {
		deps := listDeps(t, goTool, sdk+p)
		if slices.ContainsFunc(halves, func(h string) bool { return deps[h] }) {
			via = append(via, p)
		}
	}
	slices.Sort(via)
	return via
}

// beyondInits are the text symbols of syms under prefixes that are not a
// package's initialisation: what of a package a binary links beyond the init
// Go runs because something imports it.
func beyondInits(syms map[string]bool, prefixes []string) []string {
	full := make([]string, len(prefixes))
	for i, p := range prefixes {
		full[i] = sdk + p
	}
	return beyondInitsAt(syms, full)
}

// beyondInitsAt is beyondInits over prefixes written in full, a standard
// library package's among them.
func beyondInitsAt(syms map[string]bool, prefixes []string) []string {
	var code []string
	for s := range syms {
		for _, p := range prefixes {
			rest, ok := strings.CutPrefix(s, p)
			if ok && rest != "init" && !strings.HasPrefix(rest, "init.") {
				code = append(code, s)
			}
		}
	}
	slices.Sort(code)
	return code
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

// registrationOf is the registration package that brings the SDK package
// pkg of leaving.
func registrationOf(pkg string) string {
	for _, l := range leaving {
		if l.pkg == pkg {
			return l.by
		}
	}
	return pkg
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
// still in the core imports, its packages' inits alone (beyondInits); since
// step 6 took the mail spool out, the SDK's sql and queue are no longer in
// its graph, not even by their inits, and since step 7 neither is the SDK's
// scheduler, nor the jobs subsystem's. It builds testdata/coreprobe, the
// smallest product on the core alone: the facade imports every registration
// package until step 10, which a product built on it links.
func TestTheSmallestProductLinksOnlyTheSubsystemsStillInTheCore(t *testing.T) {
	goTool := goToList(t)
	syms := textSymbols(t, goTool, "./testdata/coreprobe")
	linked := linkedSymbols(syms, owners())
	for _, fn := range kitStoreCode {
		if syms[kitStoreSymbol(fn)] {
			t.Errorf("a product that does not import framework/kit/store links kit's own store code: %s", fn)
		}
	}
	// The core names the SDK's mail types — Mailer.Send takes a mail.Message
	// —, so the mail package and its SMTP transport stay in its graph; their
	// code is the mail subsystem's (ADR 0179 step 6).
	if code := beyondInitsAt(syms, mailTransport); len(code) > 0 {
		t.Errorf("a product that does not import framework/kit/mail links the SDK's mail transports beyond their inits: %v", code)
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
			// What a subsystem still in the core imports is initialised, and
			// nothing more of it is linked: its code is its registration
			// package's.
			if code := beyondInits(syms, owners()[owner]); len(code) > 0 {
				t.Errorf("the smallest product links code of %s beyond its packages' inits, which only %s may bring: %v", owner, registrationOf(owner), code)
				return
			}
			t.Logf("%s: %d text symbols linked through %s, still in the core, its packages' inits alone (ADR 0179 allow-list)", owner, n, strings.Join(via, ", "))
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
	// The stores do not bring the queues (ADR 0179 Q4: the queue package
	// imports the store package, never the other way) — not even the SDK's
	// queue's inits, which the mail spool brought until step 6.
	for _, owner := range []string{"framework/kit/queue", "pkg/v1/data/queue"} {
		if n := linked[owner]; n > 0 {
			t.Errorf("a product that imports framework/kit/store alone links %d text symbols of %s", n, owner)
		}
	}
	// Nor the workflows (Q4: the workflow package imports the store
	// package, never the other way).
	for _, owner := range []string{"framework/kit/workflow", "pkg/v1/app/statemachine"} {
		if n := linked[owner]; n > 0 {
			t.Errorf("a product that imports framework/kit/store alone links %d text symbols of %s", n, owner)
		}
	}
	linksNoMail(t, "framework/kit/store", syms, linked)
	linksNoJobs(t, "framework/kit/store", linked)
}

// linksNoJobs fails the test when a product that imports by, and not
// framework/kit/jobs, links anything of the jobs subsystem: its scheduler,
// or the SDK's — not even its inits (ADR 0179 step 7; no registration
// package imports framework/kit/jobs).
func linksNoJobs(t *testing.T, by string, linked map[string]int) {
	t.Helper()
	for _, owner := range []string{"framework/kit/jobs", "pkg/v1/app/scheduler"} {
		if n := linked[owner]; n > 0 {
			t.Errorf("a product that imports %s and not framework/kit/jobs links %d text symbols of %s", by, n, owner)
		}
	}
}

// linksNoMail fails the test when a product that imports by, and not
// framework/kit/mail, links anything of the mail subsystem: its outbox, the
// SDK's spool — not even its inits —, or the SDK's transports beyond their
// inits (ADR 0179 step 6; Q4: the mail package imports the store and queue
// packages, never the other way).
func linksNoMail(t *testing.T, by string, syms map[string]bool, linked map[string]int) {
	t.Helper()
	for _, owner := range []string{"framework/kit/mail", "pkg/v1/app/mail/spool"} {
		if n := linked[owner]; n > 0 {
			t.Errorf("a product that imports %s and not framework/kit/mail links %d text symbols of %s", by, n, owner)
		}
	}
	if code := beyondInitsAt(syms, mailTransport); len(code) > 0 {
		t.Errorf("a product that imports %s and not framework/kit/mail links the SDK's mail transports beyond their inits: %v", by, code)
	}
}

// The same half of §7 for step 4: testdata/queueprobe, coreprobe with
// framework/kit/queue, links the queue subsystem's brokers and the SDK's
// queue under them — and the store subsystem's engines, since the queue
// package imports the store package (ADR 0179 Q4).
func TestAProductThatImportsTheQueuesLinksThem(t *testing.T) {
	goTool := goToList(t)
	syms := textSymbols(t, goTool, "./testdata/queueprobe")
	linked := linkedSymbols(syms, owners())
	for _, owner := range []string{"framework/kit/queue", "pkg/v1/data/queue", "framework/kit/store", "pkg/v1/data/docstore"} {
		if linked[owner] == 0 {
			t.Errorf("a product that imports framework/kit/queue links no text symbol of %s", owner)
		}
	}
	if code := beyondInits(syms, owners()["pkg/v1/data/queue"]); len(code) == 0 {
		t.Error("a product that imports framework/kit/queue links nothing of the SDK's queue beyond its inits")
	}
	for _, owner := range []string{"framework/kit/workflow", "pkg/v1/app/statemachine"} {
		if n := linked[owner]; n > 0 {
			t.Errorf("a product that imports framework/kit/queue links %d text symbols of %s", n, owner)
		}
	}
	linksNoMail(t, "framework/kit/queue", syms, linked)
	linksNoJobs(t, "framework/kit/queue", linked)
}

// The same half of §7 for step 5: testdata/workflowprobe, coreprobe with
// framework/kit/workflow and two workflows over entities and states of
// distinct types, links the workflow subsystem's engine — the SDK's
// statemachine, instantiated once (ADR 0179 Q3) whatever the types — and the
// store subsystem's engines, since the workflow package imports the store
// package (Q4), and nothing of the queues.
func TestAProductThatImportsTheWorkflowsLinksThem(t *testing.T) {
	goTool := goToList(t)
	syms := textSymbols(t, goTool, "./testdata/workflowprobe")
	linked := linkedSymbols(syms, owners())
	type tc struct {
		owner  string
		linked bool
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if n := linked[c.owner]; (n > 0) != c.linked {
			t.Errorf("a product that imports framework/kit/workflow links %d text symbols of %s, want linked %v", n, c.owner, c.linked)
		}
	}
	for _, c := range []tc{
		{"framework/kit/workflow", true},
		{"pkg/v1/app/statemachine", true},
		{"framework/kit/store", true},
		{"pkg/v1/data/docstore", true},
		{"framework/kit/queue", false},
		{"pkg/v1/data/queue", false},
	} {
		t.Run(c.owner, func(t *testing.T) { runCase(t, c) })
	}
	linksNoMail(t, "framework/kit/workflow", syms, linked)
	linksNoJobs(t, "framework/kit/workflow", linked)
	// The probe's two workflows are compiled — the facade at each of their
	// types —, and the SDK's machine at kit's entity and the state's
	// spelling alone: its methods' wrappers name no other type, its shaped
	// bodies (go.shape.…) are compiled once for them.
	instantiated := func(prefix string) []string {
		var types []string
		for s := range syms {
			if rest, ok := strings.CutPrefix(s, sdk+prefix); ok && !strings.HasPrefix(rest, "go.shape.") {
				args, _, _ := strings.Cut(rest, "]")
				if !slices.Contains(types, args) {
					types = append(types, args)
				}
			}
		}
		slices.Sort(types)
		return types
	}
	if got := instantiated("framework/internal/kit.(*WorkflowService["); len(got) < 2 {
		t.Errorf("the probe's two workflows are not both compiled: the facade is instantiated at %q", got)
	}
	if got, want := instantiated("internal/service/app/statemachine.(*StateMachine["), sdk+"framework/internal/kit/plug.WorkflowEntityValue,string"; !slices.Equal(got, []string{want}) {
		t.Errorf("the workflow engine is instantiated at %q, want once, at %s", got, want)
	}
}

// mailTransport are the symbol prefixes of the SDK's mail transports, which
// the core no longer links (ADR 0179 step 6): the SDK's mail service half —
// the SMTP transport, the capture transport, the guards — and net/smtp under
// it. The core still imports pkg/v1/app/mail for the types Mailer.Send names,
// and through it both, so their inits stay.
var mailTransport = []string{sdk + "internal/service/app/mail.", "net/smtp."}

// The same half of §7 for step 6: testdata/mailprobe, coreprobe with
// framework/kit/mail and a mailer, links the mail subsystem's outbox — the
// SDK's spool — and its transports, and the queue and store subsystems'
// engines, since the mail package imports both (ADR 0179 Q4), and nothing of
// the workflows.
func TestAProductThatImportsTheMailLinksIt(t *testing.T) {
	goTool := goToList(t)
	syms := textSymbols(t, goTool, "./testdata/mailprobe")
	linked := linkedSymbols(syms, owners())
	type tc struct {
		owner  string
		linked bool
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if n := linked[c.owner]; (n > 0) != c.linked {
			t.Errorf("a product that imports framework/kit/mail links %d text symbols of %s, want linked %v", n, c.owner, c.linked)
		}
	}
	for _, c := range []tc{
		{"framework/kit/mail", true},
		{"pkg/v1/app/mail/spool", true},
		{"framework/kit/queue", true},
		{"pkg/v1/data/queue", true},
		{"framework/kit/store", true},
		{"pkg/v1/data/docstore", true},
		{"framework/kit/workflow", false},
		{"pkg/v1/app/statemachine", false},
	} {
		t.Run(c.owner, func(t *testing.T) { runCase(t, c) })
	}
	if code := beyondInitsAt(syms, mailTransport); len(code) == 0 {
		t.Error("a product that imports framework/kit/mail links nothing of the SDK's mail transports beyond their inits")
	}
	linksNoJobs(t, "framework/kit/mail", linked)
}

// The same half of §7 for step 7: testdata/jobsprobe, coreprobe with
// framework/kit/jobs and two jobs — one every period, one on a cron
// expression —, links the jobs subsystem's scheduler and the SDK's
// scheduler under it, beyond its inits, and nothing of the other
// subsystems: the jobs package imports no other registration package.
func TestAProductThatImportsTheJobsLinksThem(t *testing.T) {
	goTool := goToList(t)
	syms := textSymbols(t, goTool, "./testdata/jobsprobe")
	linked := linkedSymbols(syms, owners())
	type tc struct {
		owner  string
		linked bool
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if n := linked[c.owner]; (n > 0) != c.linked {
			t.Errorf("a product that imports framework/kit/jobs links %d text symbols of %s, want linked %v", n, c.owner, c.linked)
		}
	}
	for _, c := range []tc{
		{"framework/kit/jobs", true},
		{"pkg/v1/app/scheduler", true},
		{"framework/kit/store", false},
		{"pkg/v1/data/docstore", false},
		{"framework/kit/queue", false},
		{"pkg/v1/data/queue", false},
		{"framework/kit/workflow", false},
		{"pkg/v1/app/statemachine", false},
		{"framework/kit/mail", false},
		{"pkg/v1/app/mail/spool", false},
	} {
		t.Run(c.owner, func(t *testing.T) { runCase(t, c) })
	}
	if code := beyondInits(syms, owners()["pkg/v1/app/scheduler"]); len(code) == 0 {
		t.Error("a product that imports framework/kit/jobs links nothing of the SDK's scheduler beyond its inits")
	}
	for _, fn := range kitStoreCode {
		if syms[kitStoreSymbol(fn)] {
			t.Errorf("a product that imports framework/kit/jobs alone links kit's own store code: %s", fn)
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
