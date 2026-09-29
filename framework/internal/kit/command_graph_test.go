package kit_test

import (
	"context"
	"errors"
	"io"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/framework/internal/kit"
	"github.com/kitsunium/sdk/framework/model"
)

// mechanicKinds are the kinds of a pipeline's steps, in order.
func mechanicKinds(p []model.Mechanic) []string {
	out := make([]string, len(p))
	for i, m := range p {
		out[i] = m.Kind
	}
	return out
}

// expectPipeline checks the steps of a node's pipeline, in order.
func expectPipeline(t *testing.T, id string, p []model.Mechanic, want ...string) {
	t.Helper()
	if got := mechanicKinds(p); !slices.Equal(got, want) {
		t.Errorf("%s runs %v, want %v", id, got, want)
	}
}

// A command's node says what it is: its contract, its mode, its pipeline in
// the order a dispatch runs it, its permissions.
func TestACommandDescribesItself(t *testing.T) {
	g := startCounter(t).Graph()
	place := g.Node("counter/command/place-order").Command
	if place.Mode != model.ModeSync || place.Auth != model.AuthRequired || place.Input == nil || place.Result == nil {
		t.Errorf("place-order: %+v", place)
	}
	if !slices.Equal(place.Permissions, []model.Permission{{Action: "place", Resource: "order"}}) {
		t.Errorf("place-order's permissions: %+v", place.Permissions)
	}
	expectPipeline(t, "place-order", place.Pipeline, "auth", "validate", "authorize")
}

// A command's rule points at its function, and its key is said; a queued
// command says where it waits, how often it is tried, how many at once.
func TestACommandDescribesItsRuleItsKeyItsQueue(t *testing.T) {
	g := startCounter(t).Graph()
	cancel := g.Node("counter/command/cancel-order").Command
	if !cancel.Key || cancel.Authorize == nil || !strings.HasSuffix(cancel.Authorize.Func, ".CounterOwns") {
		t.Errorf("cancel-order: %+v", cancel)
	}
	expectPipeline(t, "cancel-order", cancel.Pipeline, "auth", "validate", "key", "authorize")
	queued := g.Node("lab/command/queued").Command
	want := model.CommandInfo{Mode: model.ModeQueued, MaxDeliveries: 3, Parallelism: 2, Queue: "memory"}
	if got := (model.CommandInfo{Mode: queued.Mode, MaxDeliveries: queued.MaxDeliveries, Parallelism: queued.Parallelism, Queue: queued.Queue}); !reflect.DeepEqual(got, want) || queued.DeadLetters == nil {
		t.Errorf("a queued command: %+v", queued)
	}
	expectPipeline(t, "a queued command", queued.Pipeline, "key", "queue")
}

// A query's node says the same, without mode, key or queue.
func TestAQueryDescribesItself(t *testing.T) {
	g := startCounter(t).Graph()
	mine := g.Node("counter/query/my-orders").Query
	if mine.Auth != model.AuthRequired || mine.Input == nil || mine.Result == nil {
		t.Errorf("my-orders: %+v", mine)
	}
	expectPipeline(t, "my-orders", mine.Pipeline, "auth")
	expectPipeline(t, "a query with a bulkhead", g.Node("lab/query/crowded").Query.Pipeline, "bulkhead")
	expectPipeline(t, "a query with a rule", g.Node("counter/query/order").Query.Pipeline, "validate", "authorize")
}

// A queued command waits in a queue: it brings the queue connector, and is
// kept in memory — or the data directory; a synchronous one brings none.
func TestAQueuedCommandIsAQueue(t *testing.T) {
	g := startCounter(t).Graph()
	if !slices.ContainsFunc(g.Connectors, func(c model.Connector) bool {
		return c.ID == model.ConnectorQueue && slices.Contains(c.Nodes, "lab/command/queued") && !slices.Contains(c.Nodes, "counter/command/place-order")
	}) {
		t.Errorf("the queue connector: %+v", g.Connectors)
	}
	c := containerOf(g.Architecture, model.ContainerMemory)
	if c == nil || !slices.Contains(c.Nodes, "lab/command/queued") || slices.Contains(c.Nodes, "counter/command/place-order") {
		t.Errorf("the memory container: %+v", c)
	}
}

// brokenCommands declares every mistake a command, a query or an exposure
// can be declared with.
func brokenCommands() *kit.Service {
	s := kit.NewService("broken", "Commands declared wrong.")
	s.Command("nil", (func(context.Context, LabInput) (int, error))(nil))
	s.Command("answers", func(context.Context, LabInput) (int, error) { return 1, nil }, kit.Queued())
	s.Command("unqueued", func(context.Context, LabInput) (kit.EmptyValue, error) { return kit.EmptyValue{}, nil }, kit.MaxDeliveries(2))
	s.Command("none", func(context.Context, LabInput) (kit.EmptyValue, error) { return kit.EmptyValue{}, nil }, kit.Queued(), kit.Parallelism(0))
	s.Command("allowed", func(context.Context, LabInput) (int, error) { return 1, nil }).
		Allow(nil, "place", "order").
		Allow(CounterPolicy, "", "order").
		Authorize(nil).
		Key(nil)
	s.Command("twice", func(context.Context, LabInput) (int, error) { return 1, nil }).
		Authorize(func(context.Context, LabInput) error { return nil }).
		Authorize(func(context.Context, LabInput) error { return nil }).
		Key(func(in LabInput) string { return in.Key }).
		Key(func(in LabInput) string { return in.Note })
	s.Command("exposed", func(context.Context, LabInput) (int, error) { return 1, nil }).
		Expose("FETCH /x").
		Expose("POST /x", kit.Private()).
		Expose("POST /x2", kit.Name("exposed-2"), kit.Auth()).
		Expose("POST /x3", kit.Name("exposed-2"))
	s.Query("nil", (func(context.Context, LabInput) (int, error))(nil))
	return s
}

// brokenProblems starts the broken commands, and returns what the start
// refused them with.
func brokenProblems(t *testing.T) []model.Diagnostic {
	t.Helper()
	err := kit.NewApp("broken", brokenCommands()).With(kit.InMemory(), kit.Listen("127.0.0.1:0"), kit.Logs(io.Discard)).Start(t.Context())
	var de *kit.DiagnosticsError
	if !errors.As(err, &de) {
		t.Fatalf("the start: %v", err)
	}
	return de.Diagnostics
}

// Every declaration problem of commands, queries and their exposures is
// said at once.
func TestCommandDeclarationProblems(t *testing.T) {
	var said []string
	for _, d := range brokenProblems(t) {
		said = append(said, d.Message)
	}
	all := strings.Join(said, "\n")
	for _, want := range []string{
		"command broken/command/nil has a nil handler",
		"its result must be kit.Empty, not int",
		"command broken/command/unqueued is not queued",
		"command broken/command/none: MaxDeliveries and Parallelism must be at least 1",
		"broken/command/allowed is allowed by a nil policy",
		"broken/command/allowed: a permission names an action and a resource",
		"broken/command/allowed is authorized by a nil function",
		"command broken/command/allowed is keyed by a nil function",
		"broken/command/twice is given two Authorize",
		"command broken/command/twice is given two keys",
		`route "FETCH /x"`,
		"the exposure of broken/command/exposed is private",
		"the exposure of broken/command/exposed asks for authentication itself",
		`already declares a endpoint named "exposed-2"`,
		"query broken/query/nil has a nil handler",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("no problem says %q:\n%s", want, all)
		}
	}
}

// A builder's problem is said where the builder was called.
func TestABuildersProblemIsSaidWhereItIsCalled(t *testing.T) {
	where := map[string]string{
		"is allowed by a nil policy":                        "Allow(nil, ",
		"is authorized by a nil function":                   "Authorize(nil)",
		"is keyed by a nil function":                        "Key(nil)",
		"is given two keys":                                 "Key(func(in LabInput) string { return in.Note })",
		"the exposure of broken/command/exposed is private": `Expose("POST /x", kit.Private())`,
	}
	for _, d := range brokenProblems(t) {
		for text, fragment := range where {
			if want := lineIn(t, "command_graph_test.go", fragment); strings.Contains(d.Message, text) && (d.Source == nil || d.Source.Line != want) {
				t.Errorf("%q is said at %+v, want line %d", d.Message, d.Source, want)
			}
		}
	}
}

// commandSourcesPointAtTheirDeclarations is TestSourcesPointAtTheDeclarations
// for commands, queries and their exposures: each node where its
// declaration is.
func commandSourcesPointAtTheirDeclarations(t *testing.T) {
	g := startCounter(t).Graph()
	for id, text := range map[string]string{
		"counter/command/place-order":  `var CounterPlace = Counter.Command("place-order"`,
		"counter/endpoint/place-order": `Expose("POST /orders")`,
		"counter/command/cancel-order": `var CounterCancel = Counter.Command("cancel-order"`,
		"counter/query/my-orders":      `var CounterMine = Counter.Query("my-orders"`,
		"counter/endpoint/my-orders":   `Expose("GET /orders")`,
		"lab/command/keyed":            `LabKeyed = Lab.Command("keyed"`,
	} {
		n := g.Node(id)
		if want := lineIn(t, "counter_product_test.go", text); n == nil || n.Source == nil || n.Source.File != "internal/kit/counter_product_test.go" || n.Source.Line != want {
			t.Errorf("%s is declared at %+v, want line %d", id, n, want)
		}
	}
	commandFunctionsPointAtTheirCode(t, g)
}

// commandFunctionsPointAtTheirCode: a command's and a query's handler, and
// their rule, where their functions are.
func commandFunctionsPointAtTheirCode(t *testing.T, g *model.Graph) {
	t.Helper()
	for id, fn := range map[string]string{"counter/command/place-order": "func CounterPlaceOrder(", "counter/query/my-orders": "func CounterMyOrders("} {
		if h := g.Node(id).Handler; h == nil || h.Line != lineIn(t, "counter_product_test.go", fn) {
			t.Errorf("%s: its handler at %+v", id, h)
		}
	}
	owns := lineIn(t, "counter_product_test.go", "func CounterOwns(")
	if a := g.Node("counter/command/cancel-order").Command.Authorize; a == nil || a.Line != owns {
		t.Errorf("the rule of cancel-order at %+v, want line %d", a, owns)
	}
	if a := g.Node("counter/query/order").Query.Authorize; a == nil || a.Line != owns {
		t.Errorf("the rule of the order query at %+v, want line %d", a, owns)
	}
}

// A read model is a store derived from others: its node says so, and the
// projection that keeps it makes what the query reads.
func TestAReadModelIsDerived(t *testing.T) {
	app := startCounter(t)
	place(t, "alice", "tea")
	place(t, "alice", "cake")
	eventually(t, "the projection", func() bool {
		s, err := CounterTotals.Ask(as(t.Context(), "alice"), kit.EmptyValue{})
		return err == nil && len(s.Orders) == 2
	})
	g := app.Graph()
	if !g.Node("counter/store/summaries").Store.ReadModel || g.Node("counter/store/orders").Store.ReadModel {
		t.Errorf("the read model and the write model: %+v, %+v", g.Node("counter/store/summaries").Store, g.Node("counter/store/orders").Store)
	}
}

// badCart is an input its classification contradicts.
type badCart struct {
	Who string `json:"who" kit:"public,subject"`
}

// badAnswer is a result its classification contradicts.
type badAnswer struct {
	Note string `json:"note" kit:"privat"`
}

// A command's and a query's types are shown — payloads, the Studio's form —
// and a queued command's input is kept: their classification is judged at
// the start, as a store's or an endpoint's (ADR 0006).
func TestAnOperationsTypesAreClassified(t *testing.T) {
	s := kit.NewService("classified", "Operations whose types say wrong things.")
	s.Command("place", func(context.Context, badCart) (kit.EmptyValue, error) { return kit.EmptyValue{}, nil })
	s.Query("read", func(context.Context, kit.EmptyValue) (badAnswer, error) { return badAnswer{}, nil })
	err := kit.NewApp("classified", s).With(kit.InMemory(), kit.Listen("127.0.0.1:0"), kit.Logs(io.Discard)).Start(t.Context())
	var de *kit.DiagnosticsError
	if !errors.As(err, &de) || !strings.Contains(err.Error(), "cannot be public") || !strings.Contains(err.Error(), `unknown word "privat"`) {
		t.Fatalf("the start: %v", err)
	}
	for _, d := range de.Diagnostics {
		if d.Node != "classified/command/place" && d.Node != "classified/query/read" {
			t.Errorf("a problem not said on its operation: %+v", d)
		}
	}
}
