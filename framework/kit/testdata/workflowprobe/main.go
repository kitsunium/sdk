// Command workflowprobe is coreprobe with the workflow subsystem imported
// and two workflows declared over entities and states of distinct types:
// the same service and command on kit's core, and framework/kit/workflow —
// which imports framework/kit/store —, whose engine framework/kit's
// initgraph test finds linked in it, instantiated once whatever the types.
package main

import (
	"context"
	"io"
	"os"

	kit "github.com/kitsunium/sdk/framework/internal/kit"
	_ "github.com/kitsunium/sdk/framework/kit/workflow"
)

// Line is the probe's only service.
var Line = kit.NewService("line", "Prints a status line.")

var _ = Line.CLI("render", "Render the line.", func(_ context.Context, _ []string, std kit.StdioValue) int {
	if _, err := io.WriteString(std.Out, "ok\n"); err != nil {
		return 1
	}
	return 0
}, kit.DefaultCommand(), kit.FailSafe())

// Phase is the state of a Ticket, a string type.
type Phase string

// Ticket is the entity of the first workflow.
type Ticket struct {
	ID    string `json:"id"`
	Phase Phase  `json:"phase"`
}

// Step is the state of a Job, an integer type.
type Step int

// Job is the entity of the second workflow, of another shape.
type Job struct {
	Key  string `json:"key"`
	Step Step   `json:"step"`
	Runs int    `json:"runs"`
}

var tickets = Line.Store("tickets", func(t Ticket) string { return t.ID })

var jobs = Line.Store("jobs", func(j Job) string { return j.Key })

var _ = Line.Workflow("triage", tickets, func(t *Ticket) *Phase { return &t.Phase }).
	Initial("new").On("take", "new", "open")

var _ = Line.Workflow("runs", jobs, func(j *Job) *Step { return &j.Step }).
	Initial(0).On("run", 0, 1)

// App is the probe's app.
var App = kit.NewApp("workflowprobe", Line)

func main() { os.Exit(App.Main(context.Background(), os.Args[1:])) }
