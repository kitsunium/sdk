package streamengine

import (
	"github.com/kitsunium/sdk/framework/internal/app/plug"
	"github.com/kitsunium/sdk/pkg/v1/clock"
)

// : Asserts at compile time that a stream is what plug describes, and that
// the app's clock is the SDK's.
var (
	_ plug.Stream = (*stream)(nil)
	_ clock.Timed = sdkClock{}
)
