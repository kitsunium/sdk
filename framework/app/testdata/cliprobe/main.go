// Command cliprobe is the smallest product a status line is: one service,
// one default fail-safe command that writes a line. The fresh-process
// benchmark of framework/app builds and runs it.
package main

import (
	"context"
	"io"
	"os"

	"github.com/kitsunium/sdk/framework/app"
)

// Line is the probe's only service.
var Line = app.NewService("line", "Prints a status line.")

var _ = Line.CLI("render", "Render the line.", func(_ context.Context, _ []string, std app.Stdio) int {
	if _, err := io.WriteString(std.Out, "ok\n"); err != nil {
		return 1
	}
	return 0
}, app.DefaultCommand(), app.FailSafe())

// App is the probe's app.
var App = app.NewApp("cliprobe", Line)

func main() { os.Exit(App.Main(context.Background(), os.Args[1:])) }
