// Command studioprobe is coreprobe with the Studio imported: the same service
// and command on kit's core, and framework/app/studio, which plugs in the
// Studio's API and kit's sentences in the languages after English — the
// catalogues and the SDK's i18n framework/app's initgraph test finds linked
// in it, and in no probe that does not import it.
package main

import (
	"context"
	"io"
	"os"

	app "github.com/kitsunium/sdk/framework/internal/app"
	_ "github.com/kitsunium/sdk/framework/app/studio"
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
var App = app.NewApp("studioprobe", Line)

func main() { os.Exit(App.Main(context.Background(), os.Args[1:])) }
