// Command storeprobe is coreprobe with the store subsystem imported: the same
// service and command on kit's core, and framework/app/store, whose engines
// framework/app's initgraph test finds linked in it.
package main

import (
	"context"
	"io"
	"os"

	app "github.com/kitsunium/sdk/framework/internal/app"
	_ "github.com/kitsunium/sdk/framework/app/store"
)

// Line is the probe's only service.
var Line = app.NewService("line", "Prints a status line.")

var _ = Line.CLI("render", "Render the line.", func(_ context.Context, _ []string, std app.StdioValue) int {
	if _, err := io.WriteString(std.Out, "ok\n"); err != nil {
		return 1
	}
	return 0
}, app.DefaultCommand(), app.FailSafe())

// App is the probe's app.
var App = app.NewApp("storeprobe", Line)

func main() { os.Exit(App.Main(context.Background(), os.Args[1:])) }
