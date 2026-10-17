// Command coreprobe is cliprobe on kit's core alone: the same service and
// command, built on framework/internal/app without the facade, which imports
// no registration package either since ADR 0179's step 10. It is the control
// beside cliprobe of a program that imports no registration package, whose
// symbols framework/app's initgraph test reads.
package main

import (
	"context"
	"io"
	"os"

	app "github.com/kitsunium/sdk/framework/internal/app"
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
var App = app.NewApp("coreprobe", Line)

func main() { os.Exit(App.Main(context.Background(), os.Args[1:])) }
