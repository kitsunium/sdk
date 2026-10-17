package jobkit

import (
	"github.com/kitsunium/sdk/framework/internal/kit/plug"
	"github.com/kitsunium/sdk/pkg/v1/clock"
)

// : Asserts at compile time that the jobs' scheduler is what plug describes,
// and that the app's clock is the SDK's.
var (
	_ plug.JobScheduler = runner{}

	_ clock.Timed = sdkClock{}
)
