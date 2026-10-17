// Command coreprobe is cliprobe on kit's core alone: the same service and
// command, built on framework/internal/kit without the facade, which imports
// no registration package either since ADR 0179's step 10. It is the control
// beside cliprobe of a program that imports no registration package, whose
// symbols framework/kit's initgraph test reads.
package main

import (
	"context"
	"io"
	"os"

	kit "github.com/kitsunium/sdk/framework/internal/kit"
)

// Line is the probe's only service.
var Line = kit.NewService("line", "Prints a status line.")

var _ = Line.CLI("render", "Render the line.", func(_ context.Context, _ []string, std kit.StdioValue) int {
	if _, err := io.WriteString(std.Out, "ok\n"); err != nil {
		return 1
	}
	return 0
}, kit.DefaultCommand(), kit.FailSafe())

// App is the probe's app.
var App = kit.NewApp("coreprobe", Line)

func main() { os.Exit(App.Main(context.Background(), os.Args[1:])) }
