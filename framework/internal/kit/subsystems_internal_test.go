package kit

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/kitsunium/sdk/framework/internal/kit/plug"
	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/data/sql"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// A YAML configuration file read by a program that did not import
// framework/kit/config/yaml refuses the start, naming the import — kit reads
// JSON only by itself.
func TestAFileInAFormatNotImportedIsRefused(t *testing.T) {
	format, _ := configFormats.Load("yaml")
	configFormats.Delete("yaml")
	t.Cleanup(func() { configFormats.Store("yaml", format) })
	svc := NewService("news", "News.")
	svc.Setting("page-size", 10)
	app := NewApp("news", svc).With(InMemory(), Logs(io.Discard),
		ConfigFiles(fstest.MapFS{"config/config.yaml": {Data: []byte("page-size: 3\n")}}))
	err := app.Start(context.Background())
	var de *DiagnosticsError
	if !errors.As(err, &de) {
		t.Fatalf("the start: %v", err)
	}
	var said strings.Builder
	for _, d := range de.Diagnostics {
		said.WriteString(d.Message)
	}
	if !strings.Contains(said.String(), "framework/kit/config/yaml") {
		t.Errorf("the refusal does not name the import: %s", said.String())
	}
}

// A server whose program did not import framework/kit/server is refused at
// the start, naming the import: it would run and answer nothing. A daemon
// needs no such import.
func TestAServerWithoutItsPackageIsRefused(t *testing.T) {
	start := httpStart.Load()
	httpStart.Store(nil)
	t.Cleanup(func() { httpStart.Store(start) })
	app := NewApp("web", NewService("web", "Serves.")).With(InMemory(), Logs(io.Discard))
	var de *DiagnosticsError
	err := app.Start(context.Background())
	if !errors.As(err, &de) || !strings.Contains(de.Error(), "framework/kit/server") {
		t.Fatalf("a server without framework/kit/server: %v", err)
	}
	// A case of the refusal of ADR 0179 §6: the same code.
	if !errs.HasCode(err, CodeSubsystemMissing) {
		t.Errorf("a server without framework/kit/server is not refused with CodeSubsystemMissing: %v", err)
	}
	daemon := NewApp("d", NewService("d", "Runs.")).With(Profile("daemon"), InMemory(), Logs(io.Discard))
	if err := daemon.Start(context.Background()); err != nil {
		t.Fatalf("a daemon: %v", err)
	}
	if err := daemon.Stop(context.Background()); err != nil {
		t.Error(err)
	}
}

// withoutStores runs the test as a program that does not import
// framework/kit/store: the hook its package sets is nil (ADR 0179 §6), as
// the core sees it in such a program. The facade imports the package until
// ADR 0179's last step, so no product reaches the refusal yet but this way;
// the test binary plugs the subsystem in (subsystems_test.go), and the
// test takes it out until it ends.
func withoutStores(t *testing.T) {
	t.Helper()
	saved, code := plug.Stores.Load(), storeCode.Load()
	plug.Stores.Store(nil)
	storeCode.Store(nil)
	t.Cleanup(func() {
		plug.Stores.Store(saved)
		storeCode.Store(code)
	})
}

// probeOrder is the entity of the stores these tests declare.
type probeOrder struct{ ID string }

// A product that declares a store or a database without importing
// framework/kit/store is refused at the start (ADR 0179 §6): every
// declaration at once, each at its own position, naming the package to
// import, and the refusal carries CodeSubsystemMissing for errs.HasCode.
func TestADeclarationWithoutItsSubsystemIsRefused(t *testing.T) {
	withoutStores(t)
	svc := NewService("shop", "Sells.")
	svc.Store[probeOrder]("orders", func(o probeOrder) string { return o.ID })
	svc.Store[probeOrder]("returns", func(o probeOrder) string { return o.ID })
	app := NewApp("shop", svc).With(Profile("daemon"), InMemory(), Logs(io.Discard),
		Database("main", NewFakeDB(sql.DialectPostgres).Engine(), Keeps(svc)))
	err := app.Start(context.Background())
	var de *DiagnosticsError
	if !errors.As(err, &de) {
		t.Fatalf("the start: %v", err)
	}
	var missing []model.Diagnostic
	for _, d := range de.Diagnostics {
		if strings.Contains(d.Message, storePackage) {
			missing = append(missing, d)
		}
	}
	// Two stores and a database: three declarations, refused together.
	if len(missing) != 3 {
		t.Fatalf("%d declarations refused for want of %s, want 3:\n%v", len(missing), storePackage, de)
	}
	for _, d := range missing {
		if d.Node == "" || d.Source == nil || d.Source.Line == 0 || !strings.HasSuffix(d.Source.File, "subsystems_internal_test.go") {
			t.Errorf("a refusal names no declaration at its position: %+v", d)
		}
		if d.Texts["fr"] == "" {
			t.Errorf("a refusal speaks no French: %+v", d)
		}
	}
	if !slices.Equal(de.Missing, []string{storePackage}) {
		t.Errorf("the refusal's Missing = %q, want [%s]", de.Missing, storePackage)
	}
	if !errs.HasCode(err, CodeSubsystemMissing) {
		t.Errorf("the refusal does not carry CodeSubsystemMissing: %v", err)
	}
	if got, want := errs.PublicOf(err), "a declaration needs a subsystem this program does not import"; got != want {
		t.Errorf("the refusal's public text is %q, want %q", got, want)
	}
	if c, _ := errs.CodeOf(err); c.String() != "0.4.2.83" {
		t.Errorf("CodeSubsystemMissing is %s, want 0.4.2.83", c)
	}
}

// With the store subsystem plugged in — framework/kit/store's, as the test
// binary has it —, the same product starts: the check refuses nothing a
// program imports.
func TestAStoreWithItsSubsystemIsNotRefused(t *testing.T) {
	svc := NewService("shop", "Sells.")
	svc.Store[probeOrder]("orders", func(o probeOrder) string { return o.ID })
	app := NewApp("shop", svc).With(Profile("daemon"), InMemory(), Logs(io.Discard))
	if err := app.Start(context.Background()); err != nil {
		t.Fatalf("the start: %v", err)
	}
	if err := app.Stop(context.Background()); err != nil {
		t.Error(err)
	}
}

// A refusal of the declarations that misses no subsystem carries no
// CodeSubsystemMissing: the code says what the refusal is, not that there is
// one.
func TestARefusalThatMissesNoSubsystemCarriesNoCode(t *testing.T) {
	app := NewApp("twice", NewService("a", "A."), NewService("a", "A again.")).With(Profile("daemon"), InMemory(), Logs(io.Discard))
	err := app.Start(context.Background())
	var de *DiagnosticsError
	if !errors.As(err, &de) {
		t.Fatalf("the start: %v", err)
	}
	if errs.HasCode(err, CodeSubsystemMissing) || de.Missing != nil {
		t.Errorf("a service declared twice is refused with CodeSubsystemMissing (Missing %q): %v", de.Missing, err)
	}
}

// doubleSubject is a message whose type declares two subjects: what the
// classification refuses, whatever the program keeps.
type doubleSubject struct {
	A string `json:"a" kit:"subject"`
	B string `json:"b" kit:"subject"`
}

// The classification of a type a topic carries is judged with or without the
// store subsystem, and once in either: it is not store code (ADR 0179 step
// 3b).
func TestTheClassificationIsJudgedWithOrWithoutTheStores(t *testing.T) {
	judged := func(t *testing.T) {
		t.Helper()
		svc := NewService("mail", "Sends.")
		svc.Topic[doubleSubject]("letters")
		err := NewApp("mail", svc).With(Profile("daemon"), InMemory(), Logs(io.Discard)).Start(context.Background())
		var de *DiagnosticsError
		if !errors.As(err, &de) {
			t.Fatalf("a type with two subjects starts: %v", err)
		}
		n := 0
		for _, d := range de.Diagnostics {
			if strings.Contains(d.Message, "has two subject fields") {
				n++
			}
		}
		if n != 1 {
			t.Errorf("the classification's refusal is said %d times, want once:\n%v", n, de)
		}
	}
	t.Run("with the stores", judged)
	t.Run("without the stores", func(t *testing.T) {
		withoutStores(t)
		judged(t)
	})
}

// sealedProbe is the message of a topic whose values are sealed at rest.
type sealedProbe struct {
	Token string `json:"token" kit:"secret"`
}

// A topic whose messages are sealed on disk needs the store subsystem — the
// data keys it seals under are kept in kit's own store —, and is refused
// without it, as a store is (ADR 0179 §6, step 3b).
func TestASealedTopicWithoutTheStoresIsRefused(t *testing.T) {
	withoutStores(t)
	svc := NewService("vault", "Keeps.")
	svc.Topic[sealedProbe]("tokens")
	app := NewApp("vault", svc).With(Profile("daemon"), DataDir(t.TempDir()), Logs(io.Discard))
	err := app.Start(context.Background())
	var de *DiagnosticsError
	if !errors.As(err, &de) || !errs.HasCode(err, CodeSubsystemMissing) {
		t.Fatalf("a sealed topic without framework/kit/store: %v", err)
	}
	if !slices.ContainsFunc(de.Diagnostics, func(d model.Diagnostic) bool {
		return d.Node == "vault/topic/tokens" && strings.Contains(d.Message, storePackage)
	}) {
		t.Errorf("the refusal does not name the topic and the import:\n%v", de)
	}
}

// Main's commands on the stores — migrate, privacy, revisions, data and watch
// — are kit's own store code: without the store subsystem each says the
// import it needs and exits 78, and runs nothing.
func TestAStoreCommandWithoutTheStoresNamesTheImport(t *testing.T) {
	withoutStores(t)
	app := NewApp("cli", NewService("cli", "Runs.")).With(Profile("cli"), InMemory(), Logs(io.Discard))
	for _, name := range []string{"migrate", "privacy", "revisions", "data", "watch"} {
		var stdout, stderr strings.Builder
		if code := app.storeCommand(context.Background(), name, []string{"list"}, &stdout, &stderr); code != exitConfig {
			t.Errorf("%s without the stores exits %d, want %d", name, code, exitConfig)
		}
		if !strings.Contains(stderr.String(), storePackage) || stdout.Len() != 0 {
			t.Errorf("%s without the stores says %q on stderr and %q on stdout", name, stderr.String(), stdout.String())
		}
	}
}

// withoutQueues runs the test as a program that does not import
// framework/kit/queue: the hook its package sets is nil (ADR 0179 §6, step
// 4), as the core sees it in such a program; the test binary plugs the
// subsystem in (subsystems_test.go), and the test takes it out until it
// ends.
func withoutQueues(t *testing.T) {
	t.Helper()
	saved := plug.Queues.Load()
	plug.Queues.Store(nil)
	t.Cleanup(func() { plug.Queues.Store(saved) })
}

// probeKey is the input of the queued command these tests declare.
type probeKey struct {
	Key string `json:"key"`
}

// queueProduct declares, in one service its database keeps, a topic and its
// subscription, a watch, a queued command, a command that is not queued and a
// mailer — whose outbox is on the database, the app keeping its data in a
// data directory.
func queueProduct(t *testing.T) *App {
	svc := NewService("post", "Posts.")
	letters := svc.Topic[probeOrder]("letters")
	svc.Subscribe("read", letters, func(context.Context, probeOrder) error { return nil })
	svc.Watch("content", Moderated, func(context.Context, WrittenEvent) error { return nil })
	svc.Command("later", func(context.Context, probeKey) (EmptyValue, error) { return EmptyValue{}, nil }, Queued())
	svc.Command("now", func(context.Context, probeKey) (EmptyValue, error) { return EmptyValue{}, nil })
	svc.Mailer("notify", From("Post", "post@example.com"))
	return NewApp("post", svc).With(Profile("daemon"), DataDir(t.TempDir()), Logs(io.Discard),
		Database("main", NewFakeDB(sql.DialectPostgres).Engine(), Keeps(svc)))
}

// A product that declares what runs on a queue without importing
// framework/kit/queue is refused at the start (ADR 0179 §6, step 4): a
// topic, a subscription, a watch, a queued command and a mailer whose outbox
// is on a database — every declaration at once, each at its position, naming
// the package —, never a command that is not queued; the refusal carries
// CodeSubsystemMissing.
func TestAQueueWithoutItsSubsystemIsRefused(t *testing.T) {
	withoutQueues(t)
	err := queueProduct(t).Start(context.Background())
	var de *DiagnosticsError
	if !errors.As(err, &de) {
		t.Fatalf("the start: %v", err)
	}
	var refused []string
	for _, d := range de.Diagnostics {
		if !strings.Contains(d.Message, queuePackage) {
			continue
		}
		refused = append(refused, d.Node)
		if d.Source == nil || d.Source.Line == 0 || !strings.HasSuffix(d.Source.File, "subsystems_internal_test.go") {
			t.Errorf("a refusal names no declaration at its position: %+v", d)
		}
	}
	slices.Sort(refused)
	want := []string{"post/command/later", "post/mailer/notify", "post/subscription/content", "post/subscription/read", "post/topic/letters"}
	if !slices.Equal(refused, want) {
		t.Errorf("refused for want of %s: %q, want %q\n%v", queuePackage, refused, want, de)
	}
	if !slices.Contains(de.Missing, queuePackage) {
		t.Errorf("the refusal's Missing = %q, want it to hold %s", de.Missing, queuePackage)
	}
	if !errs.HasCode(err, CodeSubsystemMissing) {
		t.Errorf("the refusal does not carry CodeSubsystemMissing: %v", err)
	}
}

// With the queue subsystem plugged in — framework/kit/queue's, as the test
// binary has it —, the check refuses none of those declarations.
func TestAQueueWithItsSubsystemIsNotRefused(t *testing.T) {
	app := queueProduct(t)
	err := app.Start(context.Background())
	if err == nil {
		must(t, app.Stop(context.Background()))
	}
	var de *DiagnosticsError
	if errors.As(err, &de) && strings.Contains(de.Error(), queuePackage) {
		t.Fatalf("a product that imports framework/kit/queue is refused for want of it:\n%v", de)
	}
}

// What opens a queue in a program without the subsystem — Main's watch
// replay, a data copy — is refused with the import it needs, as the start
// would say it.
func TestAQueueOpenedWithoutItsSubsystemNamesTheImport(t *testing.T) {
	withoutQueues(t)
	a := NewApp("bare", NewService("bare", "Runs.")).With(InMemory(), Logs(io.Discard))
	_, err := a.openQueue(&nodeBase{id: "bare/subscription/read"}, "", 1)
	if !errs.HasCode(err, CodeSubsystemMissing) || !strings.Contains(errs.PrivateOf(err), queuePackage) {
		t.Errorf("a queue opened without framework/kit/queue: %v", err)
	}
}

// withoutWorkflows runs the test as a program that does not import
// framework/kit/workflow (ADR 0179 step 5): the hook its package sets is nil
// until the test ends.
func withoutWorkflows(t *testing.T) {
	t.Helper()
	saved := plug.Workflows.Load()
	plug.Workflows.Store(nil)
	t.Cleanup(func() { plug.Workflows.Store(saved) })
}

// probeTicket is the entity of the workflows these tests declare.
type probeTicket struct {
	ID    string `json:"id"`
	State string `json:"state"`
}

// workflowProduct declares two workflows over one store.
func workflowProduct() *App {
	svc := NewService("desk", "Answers.")
	tickets := svc.Store[probeTicket]("tickets", func(p probeTicket) string { return p.ID })
	state := func(p *probeTicket) *string { return &p.State }
	svc.Workflow("triage", tickets, state).Initial("new").On("take", "new", "open")
	svc.Workflow("closing", tickets, state).Initial("open").After("close", time.Hour, "open", "closed")
	return NewApp("desk", svc).With(Profile("daemon"), InMemory(), Logs(io.Discard))
}

// A product that declares a workflow without importing framework/kit/workflow
// is refused at the start (ADR 0179 §6, step 5): every workflow at once, each
// at its position, naming the package, never its store — whose subsystem the
// program imports —; the refusal carries CodeSubsystemMissing. With the
// subsystem plugged in — framework/kit/workflow's, as the test binary has
// it —, the same product starts. The cases run in turn: one takes the hook
// out of the process until it ends.
func TestAWorkflowIsRefusedWithoutItsSubsystemAlone(t *testing.T) {
	type tc struct {
		name    string
		without bool
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if c.without {
			withoutWorkflows(t)
		}
		app := workflowProduct()
		err := app.Start(context.Background())
		if !c.without {
			if err != nil {
				t.Fatalf("the start: %v", err)
			}
			must(t, app.Stop(context.Background()))
			return
		}
		var de *DiagnosticsError
		if !errors.As(err, &de) {
			t.Fatalf("the start: %v", err)
		}
		var refused []string
		for _, d := range de.Diagnostics {
			if !strings.Contains(d.Message, workflowPackage) {
				continue
			}
			refused = append(refused, d.Node)
			if d.Source == nil || d.Source.Line == 0 || !strings.HasSuffix(d.Source.File, "subsystems_internal_test.go") {
				t.Errorf("a refusal names no declaration at its position: %+v", d)
			}
		}
		slices.Sort(refused)
		if want := []string{"desk/workflow/closing", "desk/workflow/triage"}; !slices.Equal(refused, want) {
			t.Errorf("refused for want of %s: %q, want %q\n%v", workflowPackage, refused, want, de)
		}
		if !slices.Equal(de.Missing, []string{workflowPackage}) {
			t.Errorf("the refusal's Missing = %q, want [%s]", de.Missing, workflowPackage)
		}
		if !errs.HasCode(err, CodeSubsystemMissing) {
			t.Errorf("the refusal does not carry CodeSubsystemMissing: %v", err)
		}
	}
	for _, c := range []tc{{name: "without the subsystem", without: true}, {name: "with the subsystem"}} {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// A workflow started in a program without the subsystem — which the start's
// check makes unreachable — is refused with the import it needs.
func TestAWorkflowStartedWithoutItsSubsystemNamesTheImport(t *testing.T) {
	withoutWorkflows(t)
	svc := NewService("desk", "Answers.")
	tickets := svc.Store[probeTicket]("tickets", func(p probeTicket) string { return p.ID })
	w := svc.Workflow("triage", tickets, func(p *probeTicket) *string { return &p.State }).Initial("new")
	err := w.start(context.Background(), NewApp("desk", svc).With(InMemory(), Logs(io.Discard)))
	if !errs.HasCode(err, CodeSubsystemMissing) || !strings.Contains(errs.PrivateOf(err), workflowPackage) {
		t.Errorf("a workflow started without framework/kit/workflow: %v", err)
	}
}

// withoutMail runs the test as a program that does not import
// framework/kit/mail: the hook its package sets is nil (ADR 0179 §6, step
// 6), as the core sees it in such a program; the test binary plugs the
// subsystem in (subsystems_test.go), and the test takes it out until it
// ends.
func withoutMail(t *testing.T) {
	t.Helper()
	saved := plug.Mail.Load()
	plug.Mail.Store(nil)
	t.Cleanup(func() { plug.Mail.Store(saved) })
}

// mailProduct declares two mailers: notify, in a service a database keeps —
// its outbox on that database, a queue —, and remind, in a service none
// keeps, its outbox in the data directory.
func mailProduct(t *testing.T) *App {
	kept := NewService("post", "Posts.")
	kept.Mailer("notify", From("Post", "post@example.com"))
	free := NewService("desk", "Desk.")
	free.Mailer("remind")
	return NewApp("post", kept, free).With(Profile("daemon"), DataDir(t.TempDir()), Logs(io.Discard),
		Database("main", NewFakeDB(sql.DialectPostgres).Engine(), Keeps(kept)))
}

// refusedFor are the nodes de refuses for want of pkg, sorted, each checked
// to name its declaration at its position.
func refusedFor(t *testing.T, de *DiagnosticsError, pkg string) []string {
	t.Helper()
	var refused []string
	for _, d := range de.Diagnostics {
		if !strings.Contains(d.Message, pkg) {
			continue
		}
		refused = append(refused, d.Node)
		if d.Source == nil || d.Source.Line == 0 || !strings.HasSuffix(d.Source.File, "subsystems_internal_test.go") {
			t.Errorf("a refusal names no declaration at its position: %+v", d)
		}
	}
	slices.Sort(refused)
	return refused
}

// A product that declares a mailer without importing framework/kit/mail is
// refused at the start (ADR 0179 §6, step 6): every mailer at once, each at
// its position, naming the package, with CodeSubsystemMissing. The mail row
// stands beside the queue row, which refuses a mailer whose outbox is on a
// database without framework/kit/queue: without both, that mailer is refused
// for each, the other for the mail alone, and the refusal names both
// packages; with both, neither.
func TestAMailerWithoutItsSubsystemIsRefused(t *testing.T) {
	type tc struct {
		name              string
		mail, queue, want []string
		noMail, noQueues  bool
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if c.noMail {
			withoutMail(t)
		}
		if c.noQueues {
			withoutQueues(t)
		}
		app := mailProduct(t)
		err := app.Start(context.Background())
		if err == nil {
			must(t, app.Stop(context.Background()))
		}
		var de *DiagnosticsError
		if !errors.As(err, &de) {
			if len(c.want) > 0 {
				t.Fatalf("the start: %v", err)
			}
			return
		}
		if got := refusedFor(t, de, mailPackage); !slices.Equal(got, c.mail) {
			t.Errorf("refused for want of %s: %q, want %q\n%v", mailPackage, got, c.mail, de)
		}
		if got := refusedFor(t, de, queuePackage); !slices.Equal(got, c.queue) {
			t.Errorf("refused for want of %s: %q, want %q\n%v", queuePackage, got, c.queue, de)
		}
		if !slices.Equal(de.Missing, c.want) {
			t.Errorf("the refusal's Missing = %q, want %q", de.Missing, c.want)
		}
		if len(c.want) > 0 && !errs.HasCode(err, CodeSubsystemMissing) {
			t.Errorf("the refusal does not carry CodeSubsystemMissing: %v", err)
		}
	}
	both := []string{"desk/mailer/remind", "post/mailer/notify"}
	for _, c := range []tc{
		{name: "without the mail subsystem", noMail: true, mail: both, want: []string{mailPackage}},
		{
			name: "without the mail and the queues", noMail: true, noQueues: true, mail: both,
			queue: []string{"post/mailer/notify"}, want: []string{mailPackage, queuePackage},
		},
		{name: "with both"},
	} {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// A mailer started without the mail subsystem — which the start's check
// makes unreachable — says the import it needs.
func TestAMailerStartedWithoutItsSubsystemNamesTheImport(t *testing.T) {
	withoutMail(t)
	err := NewMailer().start(context.Background(), nil)
	if !errs.HasCode(err, CodeSubsystemMissing) || !strings.Contains(errs.PrivateOf(err), mailPackage) {
		t.Errorf("a mailer started without framework/kit/mail: %v", err)
	}
}

// withoutJobs runs the test as a program that does not import
// framework/kit/jobs: the hook its package sets is nil (ADR 0179 §6, step
// 7), as the core sees it in such a program; the test binary plugs the
// subsystem in (subsystems_test.go), and the test takes it out until it
// ends.
func withoutJobs(t *testing.T) {
	t.Helper()
	saved := plug.Jobs.Load()
	plug.Jobs.Store(nil)
	t.Cleanup(func() { plug.Jobs.Store(saved) })
}

// jobsProduct declares two jobs, one every period and one on a cron
// expression, in a daemon.
func jobsProduct() *App {
	svc := NewService("audit", "Audits.")
	svc.Every("tally", time.Minute, func(context.Context) error { return nil })
	svc.Cron("digest", "0 9 * * *", func(context.Context) error { return nil })
	return NewApp("audit", svc).With(Profile("daemon"), InMemory(), Logs(io.Discard))
}

// A product that declares a job without importing framework/kit/jobs is
// refused at the start (ADR 0179 §6, step 7): every job at once — an
// interval's and a cron's —, each at its position, naming the package, with
// CodeSubsystemMissing. With the subsystem plugged in — framework/kit/jobs's,
// as the test binary has it —, the same product starts, its jobs' loops on
// their schedules.
func TestAJobWithoutItsSubsystemIsRefused(t *testing.T) {
	type tc struct {
		name    string
		without bool
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if c.without {
			withoutJobs(t)
		}
		app := jobsProduct()
		err := app.Start(context.Background())
		if !c.without {
			if err != nil {
				t.Fatalf("the start: %v", err)
			}
			defer func() { must(t, app.Stop(context.Background())) }()
			for _, id := range []string{"audit/job/tally", "audit/job/digest"} {
				if l := app.loopNamed(id); l == nil || l.schedule == nil {
					t.Errorf("the job %s has no loop on its schedule: %+v", id, l)
				}
			}
			return
		}
		var de *DiagnosticsError
		if !errors.As(err, &de) {
			t.Fatalf("the start: %v", err)
		}
		if got, want := refusedFor(t, de, jobsPackage), []string{"audit/job/digest", "audit/job/tally"}; !slices.Equal(got, want) {
			t.Errorf("refused for want of %s: %q, want %q\n%v", jobsPackage, got, want, de)
		}
		if !slices.Equal(de.Missing, []string{jobsPackage}) {
			t.Errorf("the refusal's Missing = %q, want [%s]", de.Missing, jobsPackage)
		}
		if !errs.HasCode(err, CodeSubsystemMissing) {
			t.Errorf("the refusal does not carry CodeSubsystemMissing: %v", err)
		}
	}
	for _, c := range []tc{{name: "without the subsystem", without: true}, {name: "with the subsystem"}} {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// A daemon that declares no job starts without the jobs subsystem, and its
// scheduler's loop is there all the same, waiting on no entry: the
// workflows' timers start with it, and the Studio shows it as before.
func TestADaemonWithoutJobsNeedsNoJobsSubsystem(t *testing.T) {
	withoutJobs(t)
	app := NewApp("quiet", NewService("quiet", "Waits.")).With(Profile("daemon"), InMemory(), Logs(io.Discard))
	must(t, app.Start(context.Background()))
	defer func() { must(t, app.Stop(context.Background())) }()
	if l := app.loopNamed("scheduler"); l == nil || l.Schedule != "earliest of 0 entries" {
		t.Errorf("the scheduler's loop: %+v", l)
	}
}

// A job's schedule is read at the start, through the subsystem, never at
// the declaration: a job declared before the subsystem is plugged in is
// judged by it all the same, and judged by nothing without it — the start
// refuses it for want of the import. A scheduler opened for jobs without the
// subsystem — which the start's check makes unreachable — is refused with
// the import it needs.
func TestAJobIsJudgedAtTheStartThroughItsSubsystem(t *testing.T) {
	withoutJobs(t)
	svc := NewService("audit", "Audits.")
	every := svc.Every("tally", 0, func(context.Context) error { return nil })
	cron := svc.Cron("digest", "every tuesday", func(context.Context) error { return nil })
	for _, j := range []*Job{every, cron} {
		if got := j.check(); len(got) != 0 {
			t.Errorf("the job %s judged without the subsystem: %v", j.name, got)
		}
	}
	app := NewApp("audit", svc).With(InMemory(), Logs(io.Discard))
	if _, err := app.openScheduler([]*Job{every}); !errs.HasCode(err, CodeSubsystemMissing) || !strings.Contains(errs.PrivateOf(err), jobsPackage) {
		t.Errorf("a scheduler opened without framework/kit/jobs: %v", err)
	}
	refuse := &plug.JobDriverConfig{
		Every: func(time.Duration) (plug.JobScheduleFunc, error) { return nil, errors.New("not positive") },
		Cron:  func(string) (plug.JobScheduleFunc, error) { return nil, errors.New("not five fields") },
	}
	plug.Jobs.Store(refuse)
	for _, c := range []struct {
		j    *Job
		want string
	}{{every, "period must be positive"}, {cron, "five-field cron expression"}} {
		if got := c.j.check(); len(got) != 1 || !strings.Contains(got[0].String(), c.want) {
			t.Errorf("the job %s judged once the subsystem is plugged in: %v, want %q", c.j.name, got, c.want)
		}
	}
}
