// Command otlpprobe is coreprobe with the OTLP export subsystem imported
// and an export declared: the same service and command on kit's core, and
// framework/app/otlp, whose forwarder framework/app's initgraph test finds
// linked in it.
package main

import (
	"context"
	"io"
	"os"

	app "github.com/kitsunium/sdk/framework/internal/app"
	_ "github.com/kitsunium/sdk/framework/app/otlp"
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
var App = app.NewApp("otlpprobe", Line).With(app.OTLP(app.OTLPEndpoint("http://127.0.0.1:4318")))

func main() { os.Exit(App.Main(context.Background(), os.Args[1:])) }
