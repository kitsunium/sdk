package app_test

import (
	"bufio"
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// sdk is the repository's module path, which every package below is under.
const sdk = "github.com/kitsunium/sdk/"

// registration are the registration packages of kit's subsystems: a program
// has a subsystem if and only if it imports its package (ADR 0179 §1, ADR
// 0172) — the stores, the queues, the workflows, the mailers and the jobs,
// the HTTP engine, and the Studio. Since ADR 0179's step 10 the facade
// imports none of them: a product imports those it declares, as kit gen
// writes them.
var registration = []string{
	"framework/app/store",
	"framework/app/queue",
	"framework/app/workflow",
	"framework/app/mail",
	"framework/app/jobs",
	"framework/app/server",
	"framework/app/studio",
}

// A product that imports framework/app alone initialises no subsystem (ADR
// 0172, ADR 0179 §7): neither the HTTP engine and the frontends' file server
// (framework/app/server), nor the Studio's event stream, profiler and
// catalogues after English (framework/app/studio), nor the process sampler —
// runtime/metrics, whose initialisation every process importing it pays, and
// the spawn engine behind pkg/v1/proc/process —, nor any registration
// package, any subsystem's implementation or any SDK package of leaving. Go
// runs the initialisation of every package a binary imports, used or not,
// so the import graph is what decides.
//
// It lists the graph with the go command, and skips without one or under
// Bazel, whose sandbox holds no module. Its gate is CI's test-386 job, which
// runs go test over every module with the toolchain on PATH (rule 12).
func TestTheFacadeImportsNoOptInSubsystem(t *testing.T) {
	goTool := goToList(t)
	deps := listDeps(t, goTool, sdk+"framework/app")
	if !deps[sdk+"framework/internal/app"] {
		t.Fatalf("the listing does not hold framework/internal/app: %v", deps)
	}
	for _, opt := range []struct{ pkg, by string }{
		{"github.com/kitsunium/sdk/pkg/v1/net/server", "framework/app/server"},
		{"github.com/kitsunium/sdk/pkg/v1/net/static", "framework/app/server"},
		{"github.com/kitsunium/sdk/pkg/v1/proc/process", "framework/app/server"},
		{"github.com/kitsunium/sdk/internal/service/proc/exec", "framework/app/server"},
		{"runtime/metrics", "framework/app/server"},
		{"github.com/kitsunium/sdk/pkg/v1/net/sse", "framework/app/studio"},
		{"github.com/kitsunium/sdk/pkg/v1/observe/profiling", "framework/app/studio"},
	} {
		if deps[opt.pkg] {
			t.Errorf("framework/app imports %s, which only %s may bring", opt.pkg, opt.by)
		}
	}
	for _, by := range registration {
		if deps[sdk+by] {
			t.Errorf("framework/app imports the registration package %s: a product imports it, the facade never (ADR 0179 step 10)", by)
		}
	}
	for by, engine := range kitEngines {
		if pkg := sdk + strings.TrimSuffix(engine, "."); deps[pkg] {
			t.Errorf("framework/app imports %s, which only %s may bring", pkg, by)
		}
	}
	if deps[sdk+strings.TrimSuffix(studioKit, ".")] {
		t.Errorf("framework/app imports %s, which only framework/app/studio may bring", strings.TrimSuffix(studioKit, "."))
	}
	for _, l := range leaving {
		for _, pkg := range l.packages() {
			if deps[sdk+pkg] {
				t.Errorf("framework/app imports %s, which only %s may bring (ADR 0179 step %d)", pkg, l.by, l.step)
			}
		}
	}
}

// leaving are the subsystems ADR 0179 §1 took out of framework/internal/app,
// each by the SDK package its engine lives in: the registration package that
// brings it, the migration step that moved it, and the packages whose
// machine code is the subsystem's in a binary — the SDK package's core and
// service halves, whose names its symbols carry.
var leaving = []leavingCase{
	{"pkg/v1/data/docstore", "framework/app/store", 3, []string{"internal/core/data/docstore.", "internal/service/data/docstore."}},
	{"pkg/v1/data/sql", "framework/app/store", 3, []string{"internal/core/data/sql.", "internal/service/data/sql."}},
	{"pkg/v1/data/queue", "framework/app/queue", 4, []string{"internal/core/data/queue.", "internal/service/data/queue."}},
	{"pkg/v1/app/statemachine", "framework/app/workflow", 5, []string{"internal/core/app/statemachine.", "internal/service/app/statemachine."}},
	{"pkg/v1/app/mail/spool", "framework/app/mail", 6, []string{"internal/core/app/mail/spool.", "internal/service/app/mail/spool."}},
	{"pkg/v1/app/scheduler", "framework/app/jobs", 7, []string{"internal/core/app/scheduler.", "internal/service/app/scheduler."}},
	{"pkg/v1/app/i18n", "framework/app/studio", 8, []string{"internal/core/app/i18n.", "internal/service/app/i18n."}},
}

// leavingCase is one subsystem of leaving: the SDK package its engine lives
// in, its registration package, its step, and the prefixes of its symbols.
type leavingCase struct {
	pkg, by string
	step    int
	code    []string
}

// packages are the SDK package of l and its core and service halves.
func (l leavingCase) packages() []string {
	out := []string{l.pkg}
	for _, c := range l.code {
		out = append(out, strings.TrimSuffix(c, "."))
	}
	return out
}

// kitEngines are the implementation packages of the subsystems that left
// the core, by the registration package that alone imports each: their
// symbols in a binary are the subsystem's (ADR 0179 §1, step 3 on).
var kitEngines = map[string]string{
	"framework/app/store":    "framework/internal/app/storeengine.",
	"framework/app/queue":    "framework/internal/app/queueengine.",
	"framework/app/workflow": "framework/internal/app/workflowengine.",
	"framework/app/mail":     "framework/internal/app/mailengine.",
	"framework/app/jobs":     "framework/internal/app/jobengine.",
}

// studioKit is the implementation package framework/app/studio alone
// imports: the Studio's event stream, its profiler and, since ADR 0179's
// step 8, kit's catalogues after English over the SDK's i18n.
const studioKit = "framework/internal/app/studioengine."

// frenchSentence and englishSentence are a sentence of kit's French
// catalogue and the same one of its English table, as a binary holds them:
// French only when the program imports framework/app/studio, English
// always.
const (
	frenchSentence  = "doit être une durée positive"
	englishSentence = "must be a positive duration"
)

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

// The core imports none of the subsystems ADR 0179 §1 took out of it, nor
// their implementations nor their registration packages; and plug, the
// contract between the core and every subsystem, imports the standard
// library and the framework's model only (ADR 0179 §3, §7). The allow-list
// the migration ran under is gone with its step 10: there is no exception.
func TestTheCoreImportsNoSubsystem(t *testing.T) {
	goTool := goToList(t)
	core := listDeps(t, goTool, sdk+"framework/internal/app")
	for _, l := range leaving {
		for _, pkg := range l.packages() {
			if core[sdk+pkg] {
				t.Errorf("framework/internal/app imports %s, which only %s may bring once step %d of ADR 0179 moved it", pkg, l.by, l.step)
			}
		}
	}
	for by, engine := range kitEngines {
		if pkg := sdk + strings.TrimSuffix(engine, "."); core[pkg] {
			t.Errorf("framework/internal/app imports %s, which only %s may bring", pkg, by)
		}
	}
	if core[sdk+strings.TrimSuffix(studioKit, ".")] {
		t.Errorf("framework/internal/app imports %s, which only framework/app/studio may bring", strings.TrimSuffix(studioKit, "."))
	}
	for _, by := range registration {
		if core[sdk+by] {
			t.Errorf("framework/internal/app imports the registration package %s", by)
		}
	}
	for p := range goList(t, goTool, sdk+"framework/internal/app/plug", "-f", "{{join .Imports \" \"}}") {
		if strings.Contains(strings.Split(p, "/")[0], ".") && p != sdk+"framework/model" {
			t.Errorf("plug imports %s: it imports the standard library and framework/model only", p)
		}
	}
}

// textSymbols builds the program at dir and lists its text symbols: an
// import graph says what is initialised, the symbol table what is linked. It
// returns the binary's bytes too, for the data it holds.
func textSymbols(t *testing.T, goTool, dir string) (map[string]bool, []byte) {
	t.Helper()
	syms, image, _ := buildProbe(t, goTool, dir)
	return syms, image
}

// buildProbe is textSymbols with the binary's path.
func buildProbe(t *testing.T, goTool, dir string) (map[string]bool, []byte, string) {
	t.Helper()
	bin := filepath.Join(t.TempDir(), filepath.Base(dir))
	if runtime.GOOS == "windows" {
		// Windows runs a program by its extension.
		bin += ".exe"
	}
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
	image, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	return syms, image, bin
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
// framework/app/store (EnableStores, step 3b): one function of each part, by
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

// studioCode is the Studio's code ADR 0180's step 1 took out of kit's core
// into framework/internal/app/studioengine — its routes, their guard and their
// handlers, the event stream, the source endpoint, the mailbox, and the
// reading of the tool's variables — and the core's reads that only its
// routes reach, through the host EnableStudio alone builds: one function of
// each part, by its symbol under sdk — a handler that is a closure by its
// closure, which the compiler never inlines away. A product that does not import
// framework/app/studio links none of them; one that does links every one, so
// that the list cannot go stale by naming what nothing links.
var studioCode = []string{
	studioKit + "mount",
	studioKit + "studio.guard.func1",
	studioKit + "serveEvents.func1",
	studioKit + "studio.serveSource",
	studioKit + "studio.serveMails",
	studioKit + "studio.serveHeapProfile",
	studioKit + "studioEnv",
	studioKit + "studioSettings",
	studioKit + "devForced",
	studioKit + "devBuildOf",
	kitCore + "mountStudio",
	kitCore + "studioHost.Source",
	kitCore + "studioHost.Reply",
	kitCore + "(*App).studioLogs",
	kitCore + "(*App).studioDatabases",
	kitCore + "(*App).studioTrash",
	kitCore + "readSource",
}

// kitCore is kit's core, the prefix of its symbols.
const kitCore = "framework/internal/app."

// toolVariables are the names of the tool's variables (ADR 0180 §3): what
// kit dev sets for a launch and what turns the Studio on, off or remote and
// the static analysis off. Only the Studio subsystem reads them, so only a
// product that imports framework/app/studio holds them.
var toolVariables = []string{
	"KIT_DEV_BUILT_AT", "KIT_DEV_BUILD_MS", "KIT_DEV_REBUILDS", "KIT_DEV_CHANGED", "KIT_DEV_FORCED",
	"KIT_STUDIO", "KIT_ANALYZE",
}

// linksNoStudio fails the test when a product that imports by, and not
// framework/app/studio, links any of studioCode or holds the name of one of
// the tool's variables (ADR 0180 step 1).
func linksNoStudio(t *testing.T, by string, syms map[string]bool, image []byte) {
	t.Helper()
	for _, fn := range studioCode {
		if syms[sdk+fn] {
			t.Errorf("a product that imports %s and not framework/app/studio links the Studio's code: %s", by, fn)
		}
	}
	for _, name := range toolVariables {
		if bytes.Contains(image, []byte(name)) {
			t.Errorf("a product that imports %s and not framework/app/studio holds the tool's variable %s", by, name)
		}
	}
}

// kitStoreSymbol is the text symbol of one entry of kitStoreCode.
func kitStoreSymbol(fn string) string { return sdk + "framework/internal/app." + fn }

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
	out["framework/app/studio"] = []string{studioKit}
	return out
}

// A product that imports no registration package links none of the code of
// the subsystems ADR 0179 §1 took out of the core, nor the engines of those
// that left it, nor kit's own store code, nor the French catalogue: it
// speaks English from kit's own table. Since step 10 that product is one of
// the facade — testdata/cliprobe, the smallest product, which imports
// framework/app alone — as surely as one of the core alone,
// testdata/coreprobe: the facade completes nothing, and no subsystem is
// allowed in either.
func TestTheSmallestProductLinksNoSubsystem(t *testing.T) {
	goTool := goToList(t)
	for _, probe := range []string{"cliprobe", "coreprobe"} {
		t.Run(probe, func(t *testing.T) { linksNoSubsystem(t, goTool, "./testdata/"+probe) })
	}
}

// linksNoSubsystem fails the test when the program at dir links anything of
// a subsystem.
func linksNoSubsystem(t *testing.T, goTool, dir string) {
	t.Helper()
	syms, image := textSymbols(t, goTool, dir)
	linked := linkedSymbols(syms, owners())
	// kit speaks English from its own table, and no other language: the
	// catalogues after English are the Studio's (ADR 0179 step 8, Q6).
	if !bytes.Contains(image, []byte(englishSentence)) {
		t.Errorf("the smallest product does not hold the framework's English table (%q)", englishSentence)
	}
	if bytes.Contains(image, []byte(frenchSentence)) {
		t.Errorf("a product that does not import framework/app/studio holds the framework's French catalogue (%q)", frenchSentence)
	}
	for _, fn := range kitStoreCode {
		if syms[kitStoreSymbol(fn)] {
			t.Errorf("a product that does not import framework/app/store links the framework's own store code: %s", fn)
		}
	}
	linksNoStudio(t, "framework/app", syms, image)
	// The core names the SDK's mail types — Mailer.Send takes a mail.Message
	// —, so the mail package and its SMTP transport stay in its graph; their
	// code is the mail subsystem's (ADR 0179 step 6).
	if code := beyondInitsAt(syms, mailTransport); len(code) > 0 {
		t.Errorf("a product that does not import framework/app/mail links the SDK's mail transports beyond their inits: %v", code)
	}
	for owner := range owners() {
		if n := linked[owner]; n > 0 {
			t.Errorf("the smallest product links %d text symbols of %s, which only its registration package may bring", n, owner)
		}
	}
}

// missingStore is the line a program that declares a store and does not
// import its subsystem is refused with: the import it needs.
const missingStore = `add import _ "github.com/kitsunium/sdk/framework/app/store"`

// A real product that declares a store and imports framework/app alone is
// refused at its start (ADR 0179 §6, step 10): testdata/missingprobe, built
// and run as a status line runs it, exits 78 and names the store and the
// import to add — the facade completes nothing, so the refusal the start
// always had is now one a product meets. It links nothing of the stores
// either: the check reads the hook, never the subsystem's code.
func TestAProductThatDeclaresAStoreWithoutItsImportIsRefused(t *testing.T) {
	goTool := goToList(t)
	syms, _, bin := buildProbe(t, goTool, "./testdata/missingprobe")
	for _, owner := range []string{"framework/app/store", "pkg/v1/data/docstore", "pkg/v1/data/sql"} {
		if n := linkedSymbols(syms, owners())[owner]; n > 0 {
			t.Errorf("a product that does not import framework/app/store links %d text symbols of %s", n, owner)
		}
	}
	var stdout, stderr bytes.Buffer
	run := exec.Command(bin)
	run.Stdout, run.Stderr = &stdout, &stderr
	err := run.Run()
	exit, ok := errors.AsType[*exec.ExitError](err)
	if !ok || exit.ExitCode() != 78 {
		t.Fatalf("a product that declares a store without its import ran: %v\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
	}
	if stdout.Len() > 0 {
		t.Errorf("the refused command wrote on its standard output: %q", stdout.String())
	}
	if out := stderr.String(); !strings.Contains(out, "line/store/entries") || !strings.Contains(out, missingStore) {
		t.Errorf("the refusal does not name the store and the import to add:\n%s", out)
	}
}

// A product that imports a registration package links the code it brings —
// the other half of §7, so that the half above cannot pass by testing
// nothing: testdata/storeprobe, coreprobe with framework/app/store, links the
// store subsystem's engines, the SDK's document store and sql under them,
// and kit's own store code.
func TestAProductThatImportsTheStoresLinksThem(t *testing.T) {
	goTool := goToList(t)
	syms, image := textSymbols(t, goTool, "./testdata/storeprobe")
	linked := linkedSymbols(syms, owners())
	for _, owner := range []string{"framework/app/store", "pkg/v1/data/docstore", "pkg/v1/data/sql"} {
		if linked[owner] == 0 {
			t.Errorf("a product that imports framework/app/store links no text symbol of %s", owner)
		}
	}
	for _, fn := range kitStoreCode {
		if !syms[kitStoreSymbol(fn)] {
			t.Errorf("a product that imports framework/app/store does not link the framework's own store code: %s", fn)
		}
	}
	// The stores do not bring the queues (ADR 0179 Q4: the queue package
	// imports the store package, never the other way) — not even the SDK's
	// queue's inits, which the mail spool brought until step 6.
	for _, owner := range []string{"framework/app/queue", "pkg/v1/data/queue"} {
		if n := linked[owner]; n > 0 {
			t.Errorf("a product that imports framework/app/store alone links %d text symbols of %s", n, owner)
		}
	}
	// Nor the workflows (Q4: the workflow package imports the store
	// package, never the other way).
	for _, owner := range []string{"framework/app/workflow", "pkg/v1/app/statemachine"} {
		if n := linked[owner]; n > 0 {
			t.Errorf("a product that imports framework/app/store alone links %d text symbols of %s", n, owner)
		}
	}
	linksNoMail(t, "framework/app/store", syms, linked)
	linksNoJobs(t, "framework/app/store", linked)
	linksNoWords(t, "framework/app/store", image, linked)
	linksNoStudio(t, "framework/app/store", syms, image)
}

// linksNoWords fails the test when a product that imports by, and not
// framework/app/studio, links anything of kit's languages after English: the
// SDK's i18n — not even its inits —, the Studio's implementation, or the
// French catalogue (ADR 0179 step 8; no registration package imports
// framework/app/studio).
func linksNoWords(t *testing.T, by string, image []byte, linked map[string]int) {
	t.Helper()
	for _, owner := range []string{"framework/app/studio", "pkg/v1/app/i18n"} {
		if n := linked[owner]; n > 0 {
			t.Errorf("a product that imports %s and not framework/app/studio links %d text symbols of %s", by, n, owner)
		}
	}
	if bytes.Contains(image, []byte(frenchSentence)) {
		t.Errorf("a product that imports %s and not framework/app/studio holds the framework's French catalogue", by)
	}
}

// linksNoJobs fails the test when a product that imports by, and not
// framework/app/jobs, links anything of the jobs subsystem: its scheduler,
// or the SDK's — not even its inits (ADR 0179 step 7; no registration
// package imports framework/app/jobs).
func linksNoJobs(t *testing.T, by string, linked map[string]int) {
	t.Helper()
	for _, owner := range []string{"framework/app/jobs", "pkg/v1/app/scheduler"} {
		if n := linked[owner]; n > 0 {
			t.Errorf("a product that imports %s and not framework/app/jobs links %d text symbols of %s", by, n, owner)
		}
	}
}

// linksNoMail fails the test when a product that imports by, and not
// framework/app/mail, links anything of the mail subsystem: its outbox, the
// SDK's spool — not even its inits —, or the SDK's transports beyond their
// inits (ADR 0179 step 6; Q4: the mail package imports the store and queue
// packages, never the other way).
func linksNoMail(t *testing.T, by string, syms map[string]bool, linked map[string]int) {
	t.Helper()
	for _, owner := range []string{"framework/app/mail", "pkg/v1/app/mail/spool"} {
		if n := linked[owner]; n > 0 {
			t.Errorf("a product that imports %s and not framework/app/mail links %d text symbols of %s", by, n, owner)
		}
	}
	if code := beyondInitsAt(syms, mailTransport); len(code) > 0 {
		t.Errorf("a product that imports %s and not framework/app/mail links the SDK's mail transports beyond their inits: %v", by, code)
	}
}

// The same half of §7 for step 4: testdata/queueprobe, coreprobe with
// framework/app/queue, links the queue subsystem's brokers and the SDK's
// queue under them — and the store subsystem's engines, since the queue
// package imports the store package (ADR 0179 Q4).
func TestAProductThatImportsTheQueuesLinksThem(t *testing.T) {
	goTool := goToList(t)
	syms, image := textSymbols(t, goTool, "./testdata/queueprobe")
	linked := linkedSymbols(syms, owners())
	for _, owner := range []string{"framework/app/queue", "pkg/v1/data/queue", "framework/app/store", "pkg/v1/data/docstore"} {
		if linked[owner] == 0 {
			t.Errorf("a product that imports framework/app/queue links no text symbol of %s", owner)
		}
	}
	if code := beyondInits(syms, owners()["pkg/v1/data/queue"]); len(code) == 0 {
		t.Error("a product that imports framework/app/queue links nothing of the SDK's queue beyond its inits")
	}
	for _, owner := range []string{"framework/app/workflow", "pkg/v1/app/statemachine"} {
		if n := linked[owner]; n > 0 {
			t.Errorf("a product that imports framework/app/queue links %d text symbols of %s", n, owner)
		}
	}
	linksNoMail(t, "framework/app/queue", syms, linked)
	linksNoJobs(t, "framework/app/queue", linked)
	linksNoWords(t, "framework/app/queue", image, linked)
	linksNoStudio(t, "framework/app/queue", syms, image)
}

// The same half of §7 for step 5: testdata/workflowprobe, coreprobe with
// framework/app/workflow and two workflows over entities and states of
// distinct types, links the workflow subsystem's engine — the SDK's
// statemachine, instantiated once (ADR 0179 Q3) whatever the types — and the
// store subsystem's engines, since the workflow package imports the store
// package (Q4), and nothing of the queues.
func TestAProductThatImportsTheWorkflowsLinksThem(t *testing.T) {
	goTool := goToList(t)
	syms, image := textSymbols(t, goTool, "./testdata/workflowprobe")
	linked := linkedSymbols(syms, owners())
	type tc struct {
		owner  string
		linked bool
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if n := linked[c.owner]; (n > 0) != c.linked {
			t.Errorf("a product that imports framework/app/workflow links %d text symbols of %s, want linked %v", n, c.owner, c.linked)
		}
	}
	for _, c := range []tc{
		{"framework/app/workflow", true},
		{"pkg/v1/app/statemachine", true},
		{"framework/app/store", true},
		{"pkg/v1/data/docstore", true},
		{"framework/app/queue", false},
		{"pkg/v1/data/queue", false},
	} {
		t.Run(c.owner, func(t *testing.T) { runCase(t, c) })
	}
	linksNoMail(t, "framework/app/workflow", syms, linked)
	linksNoJobs(t, "framework/app/workflow", linked)
	linksNoWords(t, "framework/app/workflow", image, linked)
	linksNoStudio(t, "framework/app/workflow", syms, image)
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
	if got := instantiated("framework/internal/app.(*WorkflowService["); len(got) < 2 {
		t.Errorf("the probe's two workflows are not both compiled: the facade is instantiated at %q", got)
	}
	if got, want := instantiated("internal/service/app/statemachine.(*StateMachine["), sdk+"framework/internal/app/plug.WorkflowEntityValue,string"; !slices.Equal(got, []string{want}) {
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
// framework/app/mail and a mailer, links the mail subsystem's outbox — the
// SDK's spool — and its transports, and the queue and store subsystems'
// engines, since the mail package imports both (ADR 0179 Q4), and nothing of
// the workflows.
func TestAProductThatImportsTheMailLinksIt(t *testing.T) {
	goTool := goToList(t)
	syms, image := textSymbols(t, goTool, "./testdata/mailprobe")
	linked := linkedSymbols(syms, owners())
	type tc struct {
		owner  string
		linked bool
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if n := linked[c.owner]; (n > 0) != c.linked {
			t.Errorf("a product that imports framework/app/mail links %d text symbols of %s, want linked %v", n, c.owner, c.linked)
		}
	}
	for _, c := range []tc{
		{"framework/app/mail", true},
		{"pkg/v1/app/mail/spool", true},
		{"framework/app/queue", true},
		{"pkg/v1/data/queue", true},
		{"framework/app/store", true},
		{"pkg/v1/data/docstore", true},
		{"framework/app/workflow", false},
		{"pkg/v1/app/statemachine", false},
	} {
		t.Run(c.owner, func(t *testing.T) { runCase(t, c) })
	}
	if code := beyondInitsAt(syms, mailTransport); len(code) == 0 {
		t.Error("a product that imports framework/app/mail links nothing of the SDK's mail transports beyond their inits")
	}
	linksNoJobs(t, "framework/app/mail", linked)
	linksNoWords(t, "framework/app/mail", image, linked)
	linksNoStudio(t, "framework/app/mail", syms, image)
}

// The same half of §7 for step 7: testdata/jobsprobe, coreprobe with
// framework/app/jobs and two jobs — one every period, one on a cron
// expression —, links the jobs subsystem's scheduler and the SDK's
// scheduler under it, beyond its inits, and nothing of the other
// subsystems: the jobs package imports no other registration package.
func TestAProductThatImportsTheJobsLinksThem(t *testing.T) {
	goTool := goToList(t)
	syms, image := textSymbols(t, goTool, "./testdata/jobsprobe")
	linked := linkedSymbols(syms, owners())
	type tc struct {
		owner  string
		linked bool
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if n := linked[c.owner]; (n > 0) != c.linked {
			t.Errorf("a product that imports framework/app/jobs links %d text symbols of %s, want linked %v", n, c.owner, c.linked)
		}
	}
	for _, c := range []tc{
		{"framework/app/jobs", true},
		{"pkg/v1/app/scheduler", true},
		{"framework/app/store", false},
		{"pkg/v1/data/docstore", false},
		{"framework/app/queue", false},
		{"pkg/v1/data/queue", false},
		{"framework/app/workflow", false},
		{"pkg/v1/app/statemachine", false},
		{"framework/app/mail", false},
		{"pkg/v1/app/mail/spool", false},
	} {
		t.Run(c.owner, func(t *testing.T) { runCase(t, c) })
	}
	if code := beyondInits(syms, owners()["pkg/v1/app/scheduler"]); len(code) == 0 {
		t.Error("a product that imports framework/app/jobs links nothing of the SDK's scheduler beyond its inits")
	}
	for _, fn := range kitStoreCode {
		if syms[kitStoreSymbol(fn)] {
			t.Errorf("a product that imports framework/app/jobs alone links the framework's own store code: %s", fn)
		}
	}
	linksNoWords(t, "framework/app/jobs", image, linked)
	linksNoStudio(t, "framework/app/jobs", syms, image)
}

// The same half of §7 for step 8: testdata/studioprobe, coreprobe with
// framework/app/studio, links kit's catalogues after English — the Studio's
// implementation opening them, the SDK's i18n compiling them, beyond its
// inits — and holds the French catalogue, and nothing of the store, queue,
// workflow, mail and jobs subsystems' engines: the Studio imports no
// registration package. (Its routes on the stores reach some of kit's own
// store code, which stays in the core and answers nothing without
// framework/app/store — step 3b.)
func TestAProductThatImportsTheStudioLinksItsLanguages(t *testing.T) {
	goTool := goToList(t)
	syms, image := textSymbols(t, goTool, "./testdata/studioprobe")
	linked := linkedSymbols(syms, owners())
	type tc struct {
		owner  string
		linked bool
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if n := linked[c.owner]; (n > 0) != c.linked {
			t.Errorf("a product that imports framework/app/studio links %d text symbols of %s, want linked %v", n, c.owner, c.linked)
		}
	}
	for _, c := range []tc{
		{"framework/app/studio", true},
		{"pkg/v1/app/i18n", true},
		{"framework/app/store", false},
		{"pkg/v1/data/docstore", false},
		{"framework/app/queue", false},
		{"pkg/v1/data/queue", false},
		{"framework/app/workflow", false},
		{"pkg/v1/app/statemachine", false},
		{"framework/app/mail", false},
		{"pkg/v1/app/mail/spool", false},
		{"framework/app/jobs", false},
		{"pkg/v1/app/scheduler", false},
	} {
		t.Run(c.owner, func(t *testing.T) { runCase(t, c) })
	}
	if !syms[sdk+studioKit+"openWords"] {
		t.Error("a product that imports framework/app/studio does not link the opening of the framework's catalogues after English")
	}
	if code := beyondInits(syms, owners()["pkg/v1/app/i18n"]); len(code) == 0 {
		t.Error("a product that imports framework/app/studio links nothing of the SDK's i18n beyond its inits")
	}
	if !bytes.Contains(image, []byte(frenchSentence)) {
		t.Errorf("a product that imports framework/app/studio does not hold the framework's French catalogue (%q)", frenchSentence)
	}
	if !bytes.Contains(image, []byte(englishSentence)) {
		t.Errorf("a product that imports framework/app/studio does not hold the framework's English table (%q)", englishSentence)
	}
	// It links the Studio's routes and the reading of the tool's variables
	// (ADR 0180 step 1), which no other probe links.
	for _, fn := range studioCode {
		if !syms[sdk+fn] {
			t.Errorf("a product that imports framework/app/studio does not link the Studio's code: %s", fn)
		}
	}
	for _, name := range toolVariables {
		if !bytes.Contains(image, []byte(name)) {
			t.Errorf("a product that imports framework/app/studio does not hold the tool's variable %s", name)
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
