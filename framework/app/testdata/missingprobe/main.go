// Command missingprobe is cliprobe with a store declared and the store
// subsystem not imported: a product written against framework/app alone,
// which the facade no longer completes (ADR 0179 step 10). Its start is
// refused, naming the store and the import to add; framework/app's initgraph
// test builds and runs it.
package main

import (
	"context"
	"io"
	"os"

	"github.com/kitsunium/sdk/framework/app"
)

// Line is the probe's only service.
var Line = app.NewService("line", "Prints a status line.")

// Entry is what the probe's store keeps.
type Entry struct {
	ID string `json:"id" app:"public"`
}

// Entries is the store the program declares without importing its
// subsystem.
var Entries = Line.Store("entries", func(e Entry) string { return e.ID },
	app.RetentionByProduct("the probe keeps nothing"), app.Purpose("Show the start's refusal"))

var _ = Line.CLI("render", "Render the line.", func(_ context.Context, _ []string, std app.Stdio) int {
	if _, err := io.WriteString(std.Out, "ok\n"); err != nil {
		return 1
	}
	return 0
}, app.DefaultCommand())

// App is the probe's app.
var App = app.NewApp("missingprobe", Line)

func main() { os.Exit(App.Main(context.Background(), os.Args[1:])) }
