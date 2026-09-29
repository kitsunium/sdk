package kit_test

import (
	"context"
	"io"
	"slices"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/internal/kit"
)

// BenchmarkMainRunsADefaultCommand is what a status line pays per render on
// a warm process: Main parses the arguments, starts the app in the CLI
// profile, runs the default command and stops it — in memory, with nothing
// but the command declared. p50 and p99 are reported beside the mean, since
// a shell waits on the slow renders, not the average one (statusline's
// target: p99 under 40 ms).
func BenchmarkMainRunsADefaultCommand(b *testing.B) {
	svc := kit.NewService("line", "Prints a status line.")
	svc.CLI("render", "Render the line.", func(_ context.Context, _ []string, std kit.StdioValue) int {
		if _, err := io.WriteString(std.Err, ""); err != nil {
			return 1
		}
		return 0
	}, kit.DefaultCommand(), kit.FailSafe())
	app := kit.NewApp("statusline", svc).With(kit.InMemory(), kit.Logs(io.Discard))
	args := []string{"--width", "120"}
	took := make([]time.Duration, 0, b.N)
	b.ReportAllocs()
	for b.Loop() {
		begun := time.Now()
		if status := app.Main(b.Context(), args); status != 0 {
			b.Fatalf("Main = %d", status)
		}
		took = append(took, time.Since(begun))
	}
	slices.Sort(took)
	b.ReportMetric(float64(took[len(took)/2].Microseconds()), "p50-µs")
	b.ReportMetric(float64(took[len(took)*99/100].Microseconds()), "p99-µs")
}
