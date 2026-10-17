// Command storeprobe is coreprobe with the store subsystem imported: the same
// service and command on kit's core, and framework/kit/store, whose engines
// framework/kit's initgraph test finds linked in it.
package main

import (
	"context"
	"io"
	"os"

	kit "github.com/kitsunium/sdk/framework/internal/kit"
	_ "github.com/kitsunium/sdk/framework/kit/store"
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
var App = kit.NewApp("storeprobe", Line)

func main() { os.Exit(App.Main(context.Background(), os.Args[1:])) }
