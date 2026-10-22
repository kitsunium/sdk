package jobengine

import (
	"github.com/kitsunium/sdk/framework/internal/app/plug"
	"github.com/kitsunium/sdk/pkg/v1/app/scheduler"
	"github.com/kitsunium/sdk/pkg/v1/clock"
)

// : Asserts at compile time that the numbers plug.JobEntryValue.Overlap
// gives the overlap policies — 0 skip, 1 allow, 2 replace — are the SDK
// scheduler's: each difference, taken both ways, overflows the unsigned type
// and does not compile unless the two are equal.
const (
	_ uint8 = uint8(scheduler.OverlapSkip) - 0
	_ uint8 = 0 - uint8(scheduler.OverlapSkip)
	_ uint8 = uint8(scheduler.OverlapAllow) - 1
	_ uint8 = 1 - uint8(scheduler.OverlapAllow)
	_ uint8 = uint8(scheduler.OverlapReplace) - 2
	_ uint8 = 2 - uint8(scheduler.OverlapReplace)
)

// : Asserts at compile time that the jobs' scheduler is what plug describes,
// and that the app's clock is the SDK's.
var (
	_ plug.JobScheduler = runner{}

	_ clock.Timed = sdkClock{}
)
