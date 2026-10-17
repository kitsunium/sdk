// Command jobsprobe is coreprobe with the jobs subsystem imported and two
// jobs declared, one every period and one on a cron expression: the same
// service and command on kit's core, and framework/kit/jobs, whose
// scheduler framework/kit's initgraph test finds linked in it.
package main

import (
	"context"
	"io"
	"os"
	"time"

	kit "github.com/kitsunium/sdk/framework/internal/kit"
	_ "github.com/kitsunium/sdk/framework/kit/jobs"
)

// Line is the probe's only service.
var Line = kit.NewService("line", "Prints a status line.")

var _ = Line.CLI("render", "Render the line.", func(_ context.Context, _ []string, std kit.StdioValue) int {
	if _, err := io.WriteString(std.Out, "ok\n"); err != nil {
		return 1
	}
	return 0
}, kit.DefaultCommand(), kit.FailSafe())

var _ = Line.Every("refresh", time.Minute, func(context.Context) error { return nil })

var _ = Line.Cron("rotate", "0 3 * * *", func(context.Context) error { return nil })

// App is the probe's app.
var App = kit.NewApp("jobsprobe", Line)

func main() { os.Exit(App.Main(context.Background(), os.Args[1:])) }
