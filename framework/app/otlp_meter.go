package app

import (
	"context"

	iapp "github.com/kitsunium/sdk/framework/internal/app"
	"github.com/kitsunium/sdk/pkg/v1/observe/metrics"
)

// Meter is the product's own meter, for the app ctx runs inside: the
// counters, gauges and histograms it makes land in the OTLP export's
// metrics stream, under the app's resource and an instrumentation scope
// named after the app, collected every otlp-interval beside the framework's
// own. Without a running export — none declared, the export off, a CLI
// command, or outside an app — it is a meter that keeps nothing. An
// instrument is found again by its name and attributes, so a handler may
// ask for it at each call; its Collect is the export's.
func Meter(ctx context.Context) metrics.FullMeter {
	//: the implementation is framework/internal/app's; this facade only forwards.
	return iapp.Meter(ctx)
}
