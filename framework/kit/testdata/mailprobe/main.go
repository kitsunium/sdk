// Command mailprobe is coreprobe with the mail subsystem imported and a
// mailer declared: the same service and command on kit's core, and
// framework/kit/mail — which imports framework/kit/queue and
// framework/kit/store —, whose outbox and transports framework/kit's
// initgraph test finds linked in it.
package main

import (
	"context"
	"io"
	"os"

	kit "github.com/kitsunium/sdk/framework/internal/kit"
	_ "github.com/kitsunium/sdk/framework/kit/mail"
)

// Line is the probe's only service.
var Line = kit.NewService("line", "Prints a status line.")

var _ = Line.CLI("render", "Render the line.", func(_ context.Context, _ []string, std kit.StdioValue) int {
	if _, err := io.WriteString(std.Out, "ok\n"); err != nil {
		return 1
	}
	return 0
}, kit.DefaultCommand(), kit.FailSafe())

// Notify is the probe's mailer.
var Notify = Line.Mailer("notify", kit.From("Line", "line@example.com"))

// App is the probe's app.
var App = kit.NewApp("mailprobe", Line)

func main() { os.Exit(App.Main(context.Background(), os.Args[1:])) }
