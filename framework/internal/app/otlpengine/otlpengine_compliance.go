package otlpengine

import (
	"github.com/kitsunium/sdk/framework/internal/app/plug"
	"github.com/kitsunium/sdk/pkg/v1/clock"
)

// : Asserts at compile time that the export is what plug describes, and
// that the app's clock is the SDK's.
var (
	_ plug.OTLPExporter = (*export)(nil)

	_ clock.Timed = sdkClock{}
)
