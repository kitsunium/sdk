//go:build !race

package telemetry_test

import (
	"testing"

	"github.com/kitsunium/sdk/framework/telemetry"
)

// Emitting costs a producer no allocation: the port's one promise, measured
// with the race detector off (its instrumentation allocates), in the alloc
// lane (tools/alloc-lane-targets.txt).
func TestEmitAllocatesNothing(t *testing.T) {
	ex, err := telemetry.NewExporter(&telemetry.ExporterConfig{Path: "/tmp/never-opened.sock", Hello: telemetry.HelloValue{}})
	if err != nil {
		t.Fatal(err)
	}
	ev := telemetry.Event{Kind: telemetry.KindSpan, Op: telemetry.OpRequest, Node: 1, Duration: 42}
	for name, port := range map[string]telemetry.Emitter{"exporter": ex, "nop": telemetry.Nop} {
		if n := testing.AllocsPerRun(10000, func() { port.Emit(&ev) }); n != 0 {
			t.Errorf("%s: Emit allocates %v times", name, n)
		}
	}
}
