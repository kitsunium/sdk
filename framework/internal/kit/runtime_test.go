package kit_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/internal/kit"
	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/sql"
)

func TestStoreSemantics(t *testing.T) {
	start(t)
	ctx := t.Context()
	it := Item{ID: "item_b", Name: "b", Tags: []string{"x"}}
	if err := Items.Insert(ctx, it); err != nil {
		t.Fatal(err)
	}
	var ke *kit.Error
	if err := Items.Insert(ctx, it); !errors.As(err, &ke) || ke.Status != http.StatusConflict {
		t.Fatalf("a second insert must conflict, got %v", err)
	}
	if err := Items.Put(ctx, Item{ID: "item_a", Name: "a"}); err != nil {
		t.Fatal(err)
	}
	all, err := Items.List(ctx)
	if err != nil || len(all) != 2 || all[0].ID != "item_a" {
		t.Fatalf("List is ordered by key: %+v %v", all, err)
	}
	all[1].Tags[0] = "mutated"
	again, againErr := Items.Get(ctx, "item_b")
	if againErr != nil {
		t.Fatal(againErr)
	}
	if again.Tags[0] != "x" {
		t.Fatal("a read aliases the store: mutating it changed the stored entity")
	}
	sentinel := errors.New("refused")
	if _, err := Items.Update(ctx, "item_b", func(i *Item) error { i.Name = "changed"; return sentinel }); !errors.Is(err, sentinel) {
		t.Fatalf("Update must return fn's error, got %v", err)
	}
	if got, err := Items.Get(ctx, "item_b"); err != nil || got.Name != "b" {
		t.Fatal("a failed Update changed the entity")
	}
	if _, err := Items.Update(ctx, "item_b", func(i *Item) error { i.ID = "item_z"; return nil }); !errors.As(err, &ke) || ke.Code != kit.WireInvalid {
		t.Fatalf("an Update changing the key must be refused, got %v", err)
	}
	if err := Items.Delete(ctx, "item_missing"); !errors.As(err, &ke) || ke.Status != http.StatusNotFound {
		t.Fatalf("deleting a missing key: %v", err)
	}
	if n, err := Items.Count(ctx); err != nil || n != 2 {
		t.Fatalf("Count = %d", n)
	}
}

func TestStorePersistsAcrossRestarts(t *testing.T) {
	needsFileStore(t)
	dir := t.TempDir()
	run := func(during func()) {
		app := kit.NewApp("shop", Shop, Audit).With(kit.DataDir(dir), kit.Listen("127.0.0.1:0"), kit.Env(kit.EnvProduction), kit.Logs(io.Discard))
		if err := app.Start(t.Context()); err != nil {
			t.Fatal(err)
		}
		during()
		if err := app.Stop(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	run(func() {
		if err := Items.Put(t.Context(), Item{ID: "item_kept", Name: "kept"}); err != nil {
			t.Fatal(err)
		}
	})
	raw, err := os.ReadFile(filepath.Join(dir, "shop", "items.json"))
	if err != nil || !strings.Contains(string(raw), "item_kept") {
		t.Fatalf("the store file: %s %v", raw, err)
	}
	run(func() {
		got, err := Items.Get(t.Context(), "item_kept")
		if err != nil || got.Name != "kept" {
			t.Fatalf("after a restart: %+v %v", got, err)
		}
	})
	run(func() {
		if _, err := Items.Get(t.Context(), "item_other"); err == nil {
			t.Fatal("unexpected entity")
		}
	})
}

func TestStoreRefusesUseOutsideARunningApp(t *testing.T) {
	var ke *kit.Error
	if _, err := Items.Get(context.Background(), "x"); !errors.As(err, &ke) || ke.Code != kit.WireUnavailable {
		t.Fatalf("a store of a service no app runs must say so, got %v", err)
	}
}

func TestWorkflowEvents(t *testing.T) {
	app := start(t)
	it := create(t, app, "chair", 5)
	r := call(t, app, "POST /items/"+it.ID+"/sell", noBody)
	if r.status != http.StatusConflict || r.errorCode(t) != "conflict" {
		t.Fatalf("selling a draft must conflict: %d %s", r.status, r.body)
	}
	for _, ev := range []string{"publish", "sell"} {
		if r := call(t, app, "POST /items/"+it.ID+"/"+ev, noBody); r.status != http.StatusOK {
			t.Fatalf("%s: %d %s", ev, r.status, r.body)
		}
	}
	got, gotErr := Items.Get(t.Context(), it.ID)
	if gotErr != nil {
		t.Fatal(gotErr)
	}
	if got.State != Sold || got.SoldAt == nil {
		t.Fatalf("after sell: %+v (OnEnter must stamp SoldAt)", got)
	}
	if !Lifecycle.Can(Item{State: Draft}, "publish") || Lifecycle.Can(Item{State: Sold}, "publish") {
		t.Error("Can disagrees with the declared transitions")
	}
	counts, err := Lifecycle.Census(t.Context())
	if err != nil || counts[Sold] != 1 || counts[Draft] != 0 || len(counts) != 5 {
		t.Errorf("census %v %v: every declared state must be present", counts, err)
	}
	// OnTransition published every change; the audit subscription recorded
	// them, once each.
	eventually(t, "the audit log", func() bool {
		n, nErr := Log.Count(context.Background())
		if nErr != nil {
			t.Fatal(nErr)
		}
		return n == 3
	})
}

// A workflow's own loop fires its guards when an entity is written and its
// timers when they are due — After a duration in a state, At an instant the
// entity carries — and sleeps in between: with nothing due, it never runs.
func TestWorkflowTimerAndGuard(t *testing.T) {
	app, clk := startManual(t)
	ctx := t.Context()
	t0 := clk.Now()
	state := func(id string) State {
		it, itErr := Items.Get(context.Background(), id)
		if itErr != nil {
			t.Fatal(itErr)
		}
		return it.State
	}
	// advance moves the clock a second at a time until cond holds.
	advance := func(what string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for !cond() {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
			clk.Advance(time.Second)
			time.Sleep(5 * time.Millisecond)
		}
	}
	live := func(it Item) {
		t.Helper()
		if _, err := Lifecycle.Start(ctx, it); err != nil {
			t.Fatal(err)
		}
		if _, err := Lifecycle.Fire(ctx, it.ID, "publish"); err != nil {
			t.Fatal(err)
		}
	}
	lapse := t0.Add(30 * time.Minute)
	live(Item{ID: "item_timer", Name: "timer", Stock: 3})
	live(Item{ID: "item_lapse", Name: "lapse", Stock: 3, Expires: &lapse})
	live(Item{ID: "item_guard", Name: "guard", Stock: 1})
	if _, err := Items.Update(ctx, "item_guard", func(i *Item) error { i.Stock = 0; return nil }); err != nil {
		t.Fatal(err)
	}
	// The write wakes the loop: the guard fires, the timers wait.
	advance("the guard", func() bool { return state("item_guard") == Sold })
	if s, l := state("item_timer"), state("item_lapse"); s != Live || l != Live {
		t.Fatalf("a timer fired early: %s, %s", s, l)
	}
	loop := func() model.Loop { return loopOf(t, app, "shop/workflow/lifecycle timers") }
	advance("the loop asleep until the lapse", func() bool {
		l := loop()
		return l.State == model.LoopWaiting && l.NextRun != nil && l.NextRun.Equal(lapse)
	})
	runs := loop().Runs
	for range 10 {
		clk.Advance(time.Minute)
	}
	time.Sleep(20 * time.Millisecond)
	if n := loop().Runs; n != runs {
		t.Fatalf("the loop ran %d times with nothing due", n-runs)
	}
	// At the lapse, the item's own instant.
	clk.Advance(lapse.Sub(clk.Now()))
	advance("the lapse", func() bool { return state("item_lapse") == Expired })
	if s := state("item_timer"); s != Live {
		t.Fatalf("the timer fired early: %s", s)
	}
	// An hour after it went live, the other.
	clk.Advance(t0.Add(time.Hour).Sub(clk.Now()))
	advance("the timer", func() bool { return state("item_timer") == Expired })
	// Nothing is due any more: only a write wakes the loop now.
	advance("the loop asleep", func() bool {
		l := loop()
		return l.State == model.LoopWaiting && l.NextRun == nil
	})
	if l := loop(); l.Errors != 0 || l.Provenance != model.ProvenanceLibrary || l.Library != "sdk/v1/statemachine" {
		t.Errorf("the loop %+v", l)
	}
}

// A handler that fails is retried: delivery is at least once, and Record is
// idempotent, so the log holds each event once.
func TestSubscriptionRetries(t *testing.T) {
	app := start(t)
	it := create(t, app, "flaky", 1)
	eventually(t, "the retried delivery", func() bool {
		_, err := Log.Get(context.Background(), it.ID+".create")
		return err == nil
	})
	eventually(t, "the failed delivery in the graph", func() bool {
		n := app.Graph().Node("audit/subscription/record")
		return n.Stats != nil && n.Stats.Errors == 1 && n.Stats.Count >= 2
	})
}

func TestJobRunsOnTheSchedule(t *testing.T) {
	app, clk := startManual(t)
	create(t, app, "x", 1)
	tallies.Lock()
	tallies.counts = nil
	tallies.Unlock()
	deadline := time.Now().Add(5 * time.Second)
	for {
		tallies.Lock()
		n := len(tallies.counts)
		tallies.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the job never ran")
		}
		clk.Advance(10 * time.Second)
		time.Sleep(5 * time.Millisecond)
	}
	tallies.Lock()
	first := tallies.counts[0]
	tallies.Unlock()
	if first != 1 {
		t.Errorf("the job's in-process call counted %d items", first)
	}
	g := app.Graph()
	e := g.Edge("audit/job/tally|calls|shop/endpoint/Count")
	if e == nil || e.Observed == nil {
		t.Fatalf("the job's in-process call of an endpoint is not observed: %+v", g.Edges)
	}
	// The fire was a turn of the scheduler's own loop, which knows the next.
	if l := loopOf(t, app, "scheduler"); l.Runs < 1 || l.NextRun == nil || l.Schedule != "earliest of 1 entry" {
		t.Errorf("the scheduler %+v", l)
	}
}

func TestGraphDescribesTheProduct(t *testing.T) {
	app := start(t)
	it := create(t, app, "desk", 1)
	call(t, app, "POST /items/"+it.ID+"/publish", noBody)
	eventually(t, "the audit delivery", func() bool {
		n, nErr := Log.Count(context.Background())
		if nErr != nil {
			t.Fatal(nErr)
		}
		return n == 2
	})
	g := app.Graph()
	for _, id := range []string{
		"shop", "audit", "external", "shop/store/items", "shop/topic/events",
		"shop/workflow/lifecycle", "shop/endpoint/CreateItem", "shop/endpoint/Count", "audit/subscription/record", "audit/job/tally",
	} {
		if g.Node(id) == nil {
			t.Errorf("missing node %s", id)
		}
	}
	ep := g.Node("shop/endpoint/CreateItem").Endpoint
	if ep.Method != "POST" || ep.Path != "/items" || ep.Expose != model.ExposePublic || ep.Request == nil {
		t.Errorf("endpoint info %+v", ep)
	}
	var kinds []string
	for _, m := range ep.Pipeline {
		kinds = append(kinds, m.Kind)
	}
	if !slices.Equal(kinds, []string{"decode", "validate", "ratelimit"}) {
		t.Errorf("pipeline %v", kinds)
	}
	if ep := g.Node("members/endpoint/MemberPrice").Endpoint; ep == nil || ep.Expose != model.ExposePrivate || ep.Method != "" || ep.Path != "" {
		t.Errorf("an implementation, the one endpoint without a route: %+v", ep)
	}
	wf := g.Node("shop/workflow/lifecycle").Workflow
	if wf.Initial != "draft" || len(wf.Transitions) != 6 || wf.Store != "shop/store/items" {
		t.Errorf("workflow %+v", wf)
	}
	for _, tr := range wf.Transitions {
		if tr.Source == nil || tr.Source.Line == 0 {
			t.Errorf("transition %s has no source", tr.Event)
		}
		if tr.Trigger == model.TriggerTimer && tr.At == nil && tr.After != "1h0m0s" {
			t.Errorf("timer after %q", tr.After)
		}
		if tr.Trigger == model.TriggerTimer && tr.At != nil && (tr.After != "" || !strings.HasSuffix(tr.At.Func, ".expiry")) {
			t.Errorf("timer at %+v, after %q", tr.At, tr.After)
		}
		if tr.Trigger == model.TriggerGuard && (tr.Guard == nil || !strings.HasSuffix(tr.Guard.Func, ".soldOut")) {
			t.Errorf("guard %+v", tr.Guard)
		}
	}
	for _, want := range []struct {
		id       string
		declared bool
	}{
		{"shop/workflow/lifecycle|persists|shop/store/items", true},
		{"shop/topic/events|delivers|audit/subscription/record", true},
		{"external|calls|shop/endpoint/CreateItem", false},
		{"shop/endpoint/CreateItem|transitions|shop/workflow/lifecycle|create", false},
		{"shop/workflow/lifecycle|publishes|shop/topic/events", false},
		{"audit/subscription/record|writes|audit/store/log", false},
		{"audit/subscription/record|reads|shop/store/items", false},
	} {
		e := g.Edge(want.id)
		if e == nil {
			t.Errorf("missing edge %s", want.id)
			continue
		}
		if e.Declared != want.declared {
			t.Errorf("%s declared = %v", want.id, e.Declared)
		}
		if !want.declared && e.Observed == nil {
			t.Errorf("%s was not observed", want.id)
		}
	}
	if g.Runtime == nil || g.Runtime.Phase != model.PhaseServing {
		t.Fatalf("runtime %+v", g.Runtime)
	}
	var names []string
	for _, c := range g.Runtime.Components {
		names = append(names, c.Name)
		if c.State != model.ComponentUp {
			t.Errorf("component %s is %s", c.Name, c.State)
		}
	}
	if names[0] != "secret:members/secret/link-key" || names[1] != "store:shop/store/items" || names[len(names)-1] != "health" || !slices.Contains(names, "http") {
		t.Errorf("components out of order: %v", names)
	}
	if rev := app.Graph().Revision; rev != g.Revision {
		t.Error("the revision moved without a structural change")
	}
	if g.App.Root == "" || g.App.Env != kit.EnvDev {
		t.Errorf("app info %+v", g.App)
	}
}

// Every node points at the line that declares it, and every handler at its
// function: that is the promise of clicking a node to see its code.
func TestSourcesPointAtTheDeclarations(t *testing.T) {
	app := start(t, kit.Mount(ReviewsModule, kit.Prefix("/reviews/")))
	raw, rawErr := os.ReadFile("product_test.go")
	if rawErr != nil {
		t.Fatal(rawErr)
	}
	lines := strings.Split(string(raw), "\n")
	lineOf := func(prefix string) int {
		for i, l := range lines {
			if strings.HasPrefix(strings.TrimSpace(l), prefix) {
				return i + 1
			}
		}
		t.Fatalf("no line starts with %q", prefix)
		return 0
	}
	g := app.Graph()
	for id, prefix := range map[string]string{
		"shop":                         "var Shop = kit.NewService",
		"shop/store/items":             "var Items = Shop.Store",
		"shop/topic/events":            "var Events = Shop.Topic",
		"shop/workflow/lifecycle":      "var Lifecycle = Shop.Workflow",
		"shop/endpoint/CreateItem":     `_ = Shop.Endpoint("POST /items", CreateItem`,
		"shop/endpoint/Count":          "CountAPI = Shop.Endpoint",
		"audit/subscription/record":    `var _ = Audit.Subscribe("record"`,
		"audit/job/tally":              `var _ = Audit.Every("tally"`,
		"members/store/accounts":       "var Accounts = Members.Store",
		"members/auth/session":         "var SessionAuth = Members.AuthHandler",
		"members/mailer/mail":          "var Mail = Members.Mailer",
		"members/loop/digest":          "var Digest = Members.Loop",
		"members/loop/janitor":         "var Janitor = Members.Go",
		"members/secret/link-key":      "var LinkKey = Members.Secret",
		"shop/port/quote":              "var Quote = Shop.Port",
		"members/endpoint/MemberPrice": "var MemberQuoteAPI = Members.Implement",
	} {
		n := g.Node(id)
		if n == nil || n.Source == nil {
			t.Errorf("%s has no source", id)
			continue
		}
		if n.Source.File != "internal/kit/product_test.go" || n.Source.Line != lineOf(prefix) {
			t.Errorf("%s declared at %s:%d, want internal/kit/product_test.go:%d", id, n.Source.File, n.Source.Line, lineOf(prefix))
		}
	}
	for id, fn := range map[string]string{
		"shop/endpoint/CreateItem":     "func CreateItem(",
		"members/auth/session":         "func Authenticate(",
		"members/loop/digest":          "func RunDigest(",
		"members/loop/janitor":         "func Sweep(",
		"members/endpoint/MemberPrice": "func MemberPrice(",
	} {
		if h := g.Node(id).Handler; h == nil || h.Line != lineOf(fn) {
			t.Errorf("%s: handler source %+v, want line %d", id, h, lineOf(fn))
		}
	}
	for _, w := range g.Node("members/loop/digest").Loop.Wakes {
		if w.Kind == model.WakeDeadline && (w.Func == nil || w.Func.Line != lineOf("func nextDigest(")) {
			t.Errorf("the deadline function is at %+v, want line %d", w.Func, lineOf("func nextDigest("))
		}
	}
	for _, tr := range g.Node("shop/workflow/lifecycle").Workflow.Transitions {
		want := map[string]string{"publish": `On("publish"`, "sell": `On("sell"`, "expire": `After("expire"`, "lapse": `At("lapse"`, "sold-out": `When("sold-out"`, "retire": `On("retire"`}[tr.Event]
		if tr.Source.Line != lineOf(want) {
			t.Errorf("transition %s at line %d, want %d", tr.Event, tr.Source.Line, lineOf(want))
		}
	}
	moduleSourcesPoint(t, g)
	// A database is no node: its container points at its kit.Database.
	db, line := kit.Database("sources", kit.NewFakeDB(sql.DialectPostgres).Engine()), here()
	app = kit.NewApp("sources", kit.NewService("sources", "Declares nothing.")).With(
		kit.InMemory(), db, kit.Listen("127.0.0.1:0"), kit.Env(kit.EnvDev), kit.Analyze(false), kit.Logs(io.Discard))
	run(t, app)
	if c := containerByID(app.Graph(), "container:database:sources"); c == nil || c.Source == nil || c.Source.File != "internal/kit/runtime_test.go" || c.Source.Line != line {
		t.Errorf("the database's container points at %+v, want kit/runtime_test.go:%d", c, line)
	}
	// A password policy is no node either: its store's history points at
	// its Store.Passwords (ADR 0007).
	type key struct {
		ID   string `json:"id"`
		Hash string `json:"hash" kit:"secret"`
	}
	keys := kit.NewService("sources-keys", "Keeps a password's hash.")
	store := keys.Store("keys", func(k key) string { return k.ID })
	_, line = store.Passwords(func(k *key) *string { return &k.Hash }), here()
	app = kit.NewApp("keys", keys).With(kit.InMemory(), kit.Listen("127.0.0.1:0"), kit.Env(kit.EnvDev), kit.Analyze(false), kit.Logs(io.Discard))
	run(t, app)
	h := app.Graph().Node("sources-keys/store/keys").Store.History
	if h == nil || len(h.Passwords) != 1 || h.Passwords[0].Source == nil || h.Passwords[0].Source.File != "internal/kit/runtime_test.go" || h.Passwords[0].Source.Line != line {
		t.Errorf("the password policy points at %+v, want kit/runtime_test.go:%d", h, line)
	}
	t.Run("commands and queries", commandSourcesPointAtTheirDeclarations)
}

// moduleSourcesPoint checks what TestSourcesPointAtTheDeclarations says of
// a module: kit.NewModule, kit.Mount, and a node its service declares — a
// watch too, and its handler. It lies after the test, so that the first line
// holding what it looks for is the test's.
func moduleSourcesPoint(t *testing.T, g *model.Graph) {
	t.Helper()
	m := g.ModuleOf("reviews")
	if want := (model.Source{File: "internal/kit/module_test.go", Line: lineIn(t, "module_test.go", "var ReviewsModule = kit.NewModule")}); m == nil || m.Source == nil || *m.Source != want {
		t.Errorf("the module is declared at %+v, want %+v", m, want)
	}
	if want := (model.Source{File: "internal/kit/runtime_test.go", Line: lineIn(t, "runtime_test.go", "app := start(t, kit.Mount(ReviewsModule")}); m == nil || m.Mount == nil || *m.Mount != want {
		t.Errorf("the module is mounted at %+v, want %+v", m, want)
	}
	if n := g.Node("reviews.screening/port/screen"); n == nil || n.Source == nil || n.Source.Line != lineIn(t, "module_test.go", "var Screen = Screening.Port") {
		t.Errorf("a module's node is declared at %+v", n)
	}
	w := g.Node("reviews.screening/subscription/moderation")
	if w == nil || w.Source == nil || w.Source.File != "internal/kit/module_test.go" || w.Source.Line != lineIn(t, "module_test.go", "var Moderation = Screening.Watch") {
		t.Errorf("the watch is declared at %+v", w)
	}
	if w == nil || w.Handler == nil || w.Handler.File != "internal/kit/watch_test.go" || w.Handler.Line != lineIn(t, "watch_test.go", "func Hear(") {
		t.Errorf("the watch's handler is at %+v", w)
	}
}

func TestTracesFollowTheAsyncHop(t *testing.T) {
	app := start(t)
	it := create(t, app, "trace", 1)
	eventually(t, "the audit delivery", func() bool {
		_, err := Log.Get(context.Background(), it.ID+".create")
		return err == nil
	})
	r := call(t, app, "GET /_kit/api/traces", noBody)
	var traces []model.Trace
	r.json(t, &traces)
	var tr *model.Trace
	for i := range traces {
		if traces[i].Root == "shop/endpoint/CreateItem" {
			tr = &traces[i]
		}
	}
	if tr == nil {
		t.Fatalf("no trace rooted at the endpoint: %s", r.body)
	}
	nodes := map[string]bool{}
	for _, s := range tr.Spans {
		nodes[s.Node] = true
	}
	for _, want := range []string{"shop/workflow/lifecycle", "shop/topic/events", "audit/subscription/record", "audit/store/log"} {
		if !nodes[want] {
			t.Errorf("the trace does not reach %s: %+v", want, tr.Spans)
		}
	}
	one := call(t, app, "GET /_kit/api/traces/"+tr.TraceID, noBody)
	if one.status != http.StatusOK {
		t.Errorf("trace by id: %d", one.status)
	}
}

// Goroutine lifecycle: one goroutine reads the event stream into the channel
// and closes it when the stream ends — the test's context ends the request.
func TestLiveEvents(t *testing.T) {
	app := start(t)
	req, reqErr := http.NewRequestWithContext(t.Context(), "GET", app.URL()+"/_kit/api/events", nil)
	if reqErr != nil {
		t.Fatal(reqErr)
	}
	resp, respErr := http.DefaultClient.Do(req)
	if respErr != nil {
		t.Fatal(respErr)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type %q", ct)
	}
	events := make(chan model.Event, 64)
	go func() {
		defer close(events)
		dec := &sseReader{r: resp.Body}
		for {
			raw, err := dec.next()
			if err != nil {
				return
			}
			var e model.Event
			if json.Unmarshal(raw, &e) == nil {
				events <- e
			}
		}
	}()
	hello := <-events
	if hello.Type != model.EventHello || hello.Revision == "" {
		t.Fatalf("first event %+v", hello)
	}
	create(t, app, "live", 1)
	seen := map[model.EventType]bool{}
	timeout := time.After(5 * time.Second)
	for !seen[model.EventSpan] || !seen[model.EventTransition] || !seen[model.EventCensus] {
		select {
		case e, ok := <-events:
			if !ok {
				t.Fatal("stream closed")
			}
			seen[e.Type] = true
			if e.Span != nil && strings.Contains(e.Span.Error+strings.Join(mapValues(e.Span.Attrs), ""), "do-not-leak-7f3a9c") {
				t.Fatal("a secret reached the stream")
			}
		case <-timeout:
			t.Fatalf("saw only %v", seen)
		}
	}
}

// mapValues are m's values, in no order.
func mapValues(m map[string]string) []string {
	return slices.Collect(maps.Values(m))
}

// sseReader reads the data lines of a Server-Sent Events stream.
type sseReader struct {
	r   io.Reader
	buf []byte
}

func (s *sseReader) next() ([]byte, error) {
	for {
		if i := strings.Index(string(s.buf), "\n\n"); i >= 0 {
			block := string(s.buf[:i])
			s.buf = s.buf[i+2:]
			if data, ok := strings.CutPrefix(block, "data: "); ok {
				return []byte(data), nil
			}
			continue
		}
		chunk := make([]byte, 4096)
		n, err := s.r.Read(chunk)
		if n > 0 {
			s.buf = append(s.buf, chunk[:n]...)
		}
		if err != nil {
			return nil, err
		}
	}
}

// An open Studio holds a live event stream; draining must end it rather than
// wait for it, or every restart in kit dev would take the whole HTTP budget.
func TestStopDoesNotWaitForTheStudio(t *testing.T) {
	app := kit.NewApp("shop", Shop, Audit).With(kit.InMemory(), kit.Listen("127.0.0.1:0"), kit.Env(kit.EnvDev), kit.Analyze(false), kit.Logs(io.Discard))
	if err := app.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	resp, respErr := http.Get(app.URL() + "/_kit/api/events")
	if respErr != nil {
		t.Fatal(respErr)
	}
	defer resp.Body.Close()
	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := app.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(started); took > 2*time.Second {
		t.Fatalf("stopping with a Studio connected took %s", took)
	}
}

// A transition is one transaction (ADR 0004): one that fails undoes what its
// OnEnter hooks wrote. The retire hook deletes its own entity; the
// transition then finds it gone and never writes it back — and its failure
// undoes the hook's deletion, so the entity is left as it was, in draft,
// which the workflow still says.
func TestAFailedTransitionUndoesItsHooks(t *testing.T) {
	app := start(t)
	it := create(t, app, "doomed", 1)
	r := call(t, app, "POST /items/"+it.ID+"/retire", noBody)
	if r.status != http.StatusNotFound {
		t.Fatalf("retire answered %d %s", r.status, r.body)
	}
	got, err := Items.Get(t.Context(), it.ID)
	if err != nil || got.State != Draft {
		t.Fatalf("after the failed transition: %+v %v", got, err)
	}
	var inst []model.Instance
	eventually(t, "the workflow reads the entity again", func() bool {
		call(t, app, "GET /_kit/api/instances?workflow=shop/workflow/lifecycle", noBody).json(t, &inst)
		return slices.ContainsFunc(inst, func(i model.Instance) bool { return i.ID == it.ID && i.State == Draft.String() })
	})
}

func TestEmbeddedParamsStayOutOfTheBody(t *testing.T) {
	app := start(t)
	r := call(t, app, "POST /notes", `{"text":"hi","User":"admin"}`, "X-User", "alice")
	var got NoteInput
	r.json(t, &got)
	if r.status != http.StatusOK || got.User != "alice" || got.Text != "hi" {
		t.Fatalf("with the header: %d %s", r.status, r.body)
	}
	r = call(t, app, "POST /notes", `{"text":"hi","User":"admin"}`)
	r.json(t, &got)
	if got.User != "" {
		t.Fatalf("the body set a header-bound field of an embedded struct: %s", r.body)
	}
}

func TestEchoedInputIsBounded(t *testing.T) {
	app := start(t)
	long := strings.Repeat("x", 10_000)
	r := call(t, app, "POST /items", `{"name":"a","price":1,"`+long+`":1}`)
	if r.status != http.StatusBadRequest || len(r.body) > 400 {
		t.Fatalf("an unknown member of 10 kB came back in %d bytes: %d", len(r.body), r.status)
	}
	r = call(t, app, "GET /items/"+long, noBody)
	if r.status != http.StatusNotFound || len(r.body) > 400 {
		t.Fatalf("a 10 kB key came back in %d bytes", len(r.body))
	}
}

// A caller's traceparent is honoured, and the response names the trace the
// request joined: that is how the Studio's Try it finds its trace.
func TestTraceparentIsHonouredAndEchoed(t *testing.T) {
	app := start(t)
	const parent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	r := call(t, app, "POST /items", CreateInput{Name: "traced", Price: 1}, "traceparent", parent)
	echoed := r.header.Get("traceparent")
	if r.status != http.StatusOK || !strings.HasPrefix(echoed, "00-4bf92f3577b34da6a3ce929d0e0e4736-") || echoed == parent {
		t.Fatalf("status %d, traceparent %q", r.status, echoed)
	}
	r = call(t, app, "GET /_kit/api/traces?root=shop/endpoint/CreateItem", noBody)
	var traces []model.Trace
	r.json(t, &traces)
	if len(traces) != 1 || traces[0].TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("traces rooted at the endpoint: %s", r.body)
	}
	if r := call(t, app, "GET /_kit/api/traces?root=nowhere", noBody); string(r.body) != "[]\n" {
		t.Errorf("a root with no trace: %s", r.body)
	}
}

func TestWorkflowNamesItsStateField(t *testing.T) {
	app := start(t)
	if f := app.Graph().Node("shop/workflow/lifecycle").Workflow.Field; f != "state" {
		t.Fatalf("state field %q, want the wire name of Item.State", f)
	}
}

// Every schema of a workflow's state type lists the workflow's states.
func TestSchemasListTheWorkflowStates(t *testing.T) {
	app := start(t)
	g := app.Graph()
	want := []string{"draft", "live", "sold", "expired", "retired"}
	find := func(s *model.Schema, name string) *model.Schema {
		for _, f := range s.Fields {
			if f.Name == name {
				return f.Type
			}
		}
		return nil
	}
	if st := find(g.Node("shop/store/items").Store.Entity, "state"); st == nil || !slices.Equal(st.Enum, want) {
		t.Errorf("entity state schema %+v", st)
	}
	if st := find(g.Node("shop/endpoint/CreateItem").Endpoint.Response, "state"); st == nil || !slices.Equal(st.Enum, want) {
		t.Errorf("response state schema %+v", st)
	}
	if st := find(g.Node("shop/topic/events").Topic.Message, "to"); st == nil || !slices.Equal(st.Enum, want) {
		t.Errorf("message state schema %+v", st)
	}
}

// A rare write keeps its traces however many reads come after it: traces are
// kept per entry node.
func TestTracesAreKeptPerEntry(t *testing.T) {
	app := start(t)
	it := create(t, app, "kept", 1)
	eventually(t, "the audit delivery", func() bool {
		_, err := Log.Get(context.Background(), it.ID+".create")
		return err == nil
	})
	for range 300 {
		call(t, app, "GET /search?q=x", noBody)
	}
	r := call(t, app, "GET /_kit/api/traces?root=shop/endpoint/CreateItem", noBody)
	var traces []model.Trace
	r.json(t, &traces)
	if len(traces) != 1 {
		t.Fatalf("the write's trace was evicted by reads: %d traces", len(traces))
	}
	nodes := map[string]bool{}
	for _, s := range traces[0].Spans {
		nodes[s.Node] = true
	}
	if !nodes["audit/subscription/record"] {
		t.Error("the delivery is not part of the publisher's trace")
	}
	r = call(t, app, "GET /_kit/api/traces?root=shop/endpoint/Search", noBody)
	r.json(t, &traces)
	if len(traces) != 50 {
		t.Errorf("an entry keeps its last 50 traces, got %d", len(traces))
	}
	r = call(t, app, "GET /_kit/api/traces?root=audit/subscription/record", noBody)
	r.json(t, &traces)
	if len(traces) != 0 {
		t.Errorf("a delivery is not an entry of its own: %d traces", len(traces))
	}
}

// A refusal the caller has to fix is not the product failing: a lookup of an
// item that does not exist answers 404, and its trace stays ok — the code
// says what happened, nothing counts it as an error.
func TestARefusalIsNotAFailure(t *testing.T) {
	app := start(t)
	if r := call(t, app, "GET /items/missing", noBody); r.status != http.StatusNotFound {
		t.Fatalf("GET /items/missing answered %d %s", r.status, r.body)
	}
	r := call(t, app, "GET /_kit/api/traces?root=shop/endpoint/GetItem", noBody)
	var traces []model.Trace
	r.json(t, &traces)
	if len(traces) != 1 {
		t.Fatalf("traces rooted at GetItem: %s", r.body)
	}
	tr := traces[0]
	if tr.Status != model.StatusOK {
		t.Errorf("the trace of a 404 is %q, want ok", tr.Status)
	}
	for _, s := range tr.Spans {
		if s.Status != model.StatusOK || s.Error != "" {
			t.Errorf("span %s: status %q, error %q — a refusal is not a failure", s.Node, s.Status, s.Error)
		}
	}
	if root := tr.Spans[0]; root.Code != "not_found" {
		t.Errorf("the root span's code is %q, want not_found", root.Code)
	}
}

// In dev a span carries the line of the product's code that made its call:
// GetItem's Items.Get is where the store span of a lookup comes from. The
// Studio needs it when two rows of a sequence make the same call.
func TestASpanKnowsTheLineThatMadeItsCall(t *testing.T) {
	app := start(t)
	it := create(t, app, "sited", 1)
	if r := call(t, app, "GET /items/"+it.ID, noBody); r.status != http.StatusOK {
		t.Fatalf("GET answered %d %s", r.status, r.body)
	}
	src, srcErr := os.ReadFile("product_test.go")
	if srcErr != nil {
		t.Fatal(srcErr)
	}
	want := 0
	for i, l := range strings.Split(string(src), "\n") {
		if strings.HasPrefix(l, "func GetItem(") {
			want = i + 1
		}
	}
	r := call(t, app, "GET /_kit/api/traces?root=shop/endpoint/GetItem", noBody)
	var traces []model.Trace
	r.json(t, &traces)
	if len(traces) != 1 {
		t.Fatalf("traces rooted at GetItem: %s", r.body)
	}
	for _, s := range traces[0].Spans {
		if s.Node != "shop/store/items" {
			continue
		}
		if s.Attrs["code.filepath"] != "internal/kit/product_test.go" || s.Attrs["code.lineno"] != strconv.Itoa(want) {
			t.Fatalf("the store span says %s:%s, want internal/kit/product_test.go:%d", s.Attrs["code.filepath"], s.Attrs["code.lineno"], want)
		}
		return
	}
	t.Fatalf("no store span under GetItem: %s", r.body)
}
