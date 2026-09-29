//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /framework/kit/testdata/cliprobe .

// Command cliprobe is the smallest product a status line is: one service,
// one default fail-safe command that writes a line. The fresh-process
// benchmark of framework/kit builds and runs it.
package main

import (
	"context"
	"io"
	"os"

	"github.com/kitsunium/sdk/framework/kit"
)

// Line is the probe's only service.
var Line = kit.NewService("line", "Prints a status line.")

var _ = Line.CLI("render", "Render the line.", func(_ context.Context, _ []string, std kit.Stdio) int {
	if _, err := io.WriteString(std.Out, "ok\n"); err != nil {
		return 1
	}
	return 0
}, kit.DefaultCommand(), kit.FailSafe())

// App is the probe's app.
var App = kit.NewApp("cliprobe", Line)

func main() { os.Exit(App.Main(context.Background(), os.Args[1:])) }
