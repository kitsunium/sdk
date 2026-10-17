// Command studioprobe is coreprobe with the Studio imported: the same service
// and command on kit's core, and framework/kit/studio, which plugs in the
// Studio's API and kit's sentences in the languages after English — the
// catalogues and the SDK's i18n framework/kit's initgraph test finds linked
// in it, and in no probe that does not import it.
package main

import (
	"context"
	"io"
	"os"

	kit "github.com/kitsunium/sdk/framework/internal/kit"
	_ "github.com/kitsunium/sdk/framework/kit/studio"
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
var App = kit.NewApp("studioprobe", Line)

func main() { os.Exit(App.Main(context.Background(), os.Args[1:])) }
