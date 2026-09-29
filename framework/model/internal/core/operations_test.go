package core_test

import (
	"strings"
	"testing"

	model "github.com/kitsunium/sdk/framework/model/internal/core"
)

// Commands and queries (ADR 0005) in the graph: a counter that never moves
// the revision, their shapes, and the rule's range a merge keeps.

// A queued command's dead letters are counted on its node, like a
// subscription's: a counter, which never moves the revision.
func TestRevisionIgnoresACommandsDeadLetters(t *testing.T) {
	command := func(dead *int) *model.GraphMessage {
		g := sample()
		g.Nodes = append(g.Nodes, model.NodeEntity{
			ID: "todos/command/reindex", Kind: model.KindCommand, Name: "reindex", Service: "todos",
			Command: &model.CommandSpec{Mode: model.ModeQueued, Queue: "memory", DeadLetters: dead},
		})
		g.Normalize()
		return g
	}
	three := 3
	if a, b := command(nil), command(&three); a.Revision != b.Revision {
		t.Errorf("dead letters moved the revision: %s vs %s", a.Revision, b.Revision)
	}
	if b := command(&three); b.Node("todos/command/reindex").Command.DeadLetters == nil {
		t.Error("the revision's hash took the dead letters off the graph itself")
	}
}

// A command is a parallelogram leaning back, marked when it is queued; a
// query a rhombus; an exposure dispatches its command, declared.
func TestMermaidDrawsCommandsAndQueries(t *testing.T) {
	g := &model.GraphMessage{
		Nodes: []model.NodeEntity{
			{ID: "orders", Kind: model.KindService, Name: "orders"},
			{ID: "orders/command/place-order", Kind: model.KindCommand, Name: "place-order", Service: "orders", Command: &model.CommandSpec{Mode: model.ModeSync}},
			{ID: "orders/command/reindex", Kind: model.KindCommand, Name: "reindex", Service: "orders", Command: &model.CommandSpec{Mode: model.ModeQueued}},
			{ID: "orders/query/my-orders", Kind: model.KindQuery, Name: "my-orders", Service: "orders", Query: &model.QuerySpec{}},
			{
				ID: "orders/endpoint/place-order", Kind: model.KindEndpoint, Name: "place-order", Service: "orders",
				Endpoint: &model.EndpointSpec{Method: "POST", Path: "/orders", Exposes: "orders/command/place-order"},
			},
		},
		Edges: []model.EdgeMessage{
			{From: "orders/endpoint/place-order", To: "orders/command/place-order", Kind: model.EdgeDispatches, Declared: true},
			{From: "orders/command/place-order", To: "orders/query/my-orders", Kind: model.EdgeAsks, Static: []model.SourceMessage{{File: "orders/place.go", Line: 12}}},
		},
	}
	g.Normalize()
	out := model.Mermaid(g)
	for _, want := range []string{`[\"place-order"\]`, `[\"reindex · queued"\]`, `{"my-orders"}`, `["POST /orders"]`, "-->|dispatches|", "-.->|asks|"} {
		if !strings.Contains(out, want) {
			t.Errorf("mermaid output lacks %q:\n%s", want, out)
		}
	}
}

// The runtime knows where an authorization function starts; the analysis
// reads its whole range, which the merged graph keeps — without touching the
// base graph it shares its nodes with.
func TestMergeWidensAnAuthorizationFunction(t *testing.T) {
	base := &model.GraphMessage{Nodes: []model.NodeEntity{
		{ID: "orders/command/cancel", Kind: model.KindCommand, Command: &model.CommandSpec{Mode: model.ModeSync, Authorize: &model.SourceMessage{File: "orders/cancel.go", Line: 20}}},
		{ID: "orders/query/mine", Kind: model.KindQuery, Query: &model.QuerySpec{Authorize: &model.SourceMessage{File: "orders/mine.go", Line: 7}}},
	}}
	extra := &model.GraphMessage{Nodes: []model.NodeEntity{
		{ID: "orders/command/cancel", Kind: model.KindCommand, Command: &model.CommandSpec{Authorize: &model.SourceMessage{File: "orders/cancel.go", Line: 20, EndLine: 26, Func: "example.com/orders.ownsOrder"}}},
		{ID: "orders/query/mine", Kind: model.KindQuery, Query: &model.QuerySpec{Authorize: &model.SourceMessage{File: "orders/mine.go", Line: 7, EndLine: 9}}},
	}}
	g := model.Merge(base, extra)
	if a := g.Node("orders/command/cancel").Command.Authorize; a.EndLine != 26 || a.Func != "example.com/orders.ownsOrder" {
		t.Errorf("the command's authorization: %+v", a)
	}
	if a := g.Node("orders/query/mine").Query.Authorize; a.EndLine != 9 {
		t.Errorf("the query's authorization: %+v", a)
	}
	if base.Nodes[0].Command.Authorize.EndLine != 0 {
		t.Error("the merge changed the base graph")
	}
}
