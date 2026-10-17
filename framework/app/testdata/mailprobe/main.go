// Command mailprobe is coreprobe with the mail subsystem imported and a
// mailer declared: the same service and command on kit's core, and
// framework/app/mail — which imports framework/app/queue and
// framework/app/store —, whose outbox and transports framework/app's
// initgraph test finds linked in it.
package main

import (
	"context"
	"io"
	"os"

	app "github.com/kitsunium/sdk/framework/internal/app"
	_ "github.com/kitsunium/sdk/framework/app/mail"
)

// Line is the probe's only service.
var Line = app.NewService("line", "Prints a status line.")

var _ = Line.CLI("render", "Render the line.", func(_ context.Context, _ []string, std app.StdioValue) int {
	if _, err := io.WriteString(std.Out, "ok\n"); err != nil {
		return 1
	}
	return 0
}, app.DefaultCommand(), app.FailSafe())

// Notify is the probe's mailer.
var Notify = Line.Mailer("notify", app.From("Line", "line@example.com"))

// App is the probe's app.
var App = app.NewApp("mailprobe", Line)

func main() { os.Exit(App.Main(context.Background(), os.Args[1:])) }
