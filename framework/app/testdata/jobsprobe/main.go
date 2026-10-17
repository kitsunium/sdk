// Command jobsprobe is coreprobe with the jobs subsystem imported and two
// jobs declared, one every period and one on a cron expression: the same
// service and command on kit's core, and framework/app/jobs, whose
// scheduler framework/app's initgraph test finds linked in it.
package main

import (
	"context"
	"io"
	"os"
	"time"

	app "github.com/kitsunium/sdk/framework/internal/app"
	_ "github.com/kitsunium/sdk/framework/app/jobs"
)

// Line is the probe's only service.
var Line = app.NewService("line", "Prints a status line.")

var _ = Line.CLI("render", "Render the line.", func(_ context.Context, _ []string, std app.StdioValue) int {
	if _, err := io.WriteString(std.Out, "ok\n"); err != nil {
		return 1
	}
	return 0
}, app.DefaultCommand(), app.FailSafe())

var _ = Line.Every("refresh", time.Minute, func(context.Context) error { return nil })

var _ = Line.Cron("rotate", "0 3 * * *", func(context.Context) error { return nil })

// App is the probe's app.
var App = app.NewApp("jobsprobe", Line)

func main() { os.Exit(App.Main(context.Background(), os.Args[1:])) }
