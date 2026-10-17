// Command missingprobe is cliprobe with a store declared and the store
// subsystem not imported: a product written against framework/kit alone,
// which the facade no longer completes (ADR 0179 step 10). Its start is
// refused, naming the store and the import to add; framework/kit's initgraph
// test builds and runs it.
package main

import (
	"context"
	"io"
	"os"

	"github.com/kitsunium/sdk/framework/kit"
)

// Line is the probe's only service.
var Line = kit.NewService("line", "Prints a status line.")

// Entry is what the probe's store keeps.
type Entry struct {
	ID string `json:"id" kit:"public"`
}

// Entries is the store the program declares without importing its
// subsystem.
var Entries = Line.Store("entries", func(e Entry) string { return e.ID },
	kit.RetentionByProduct("the probe keeps nothing"), kit.Purpose("Show the start's refusal"))

var _ = Line.CLI("render", "Render the line.", func(_ context.Context, _ []string, std kit.Stdio) int {
	if _, err := io.WriteString(std.Out, "ok\n"); err != nil {
		return 1
	}
	return 0
}, kit.DefaultCommand())

// App is the probe's app.
var App = kit.NewApp("missingprobe", Line)

func main() { os.Exit(App.Main(context.Background(), os.Args[1:])) }
