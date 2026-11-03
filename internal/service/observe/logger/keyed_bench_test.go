package logger_test

import (
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/service/observe/logger/middleware/sample"
)

// BenchmarkMW_SampleKeyed_Kept measures the keyed sampler on a record it
// forwards: the key's lookup, the window check under the lock, the forward
// (#1017). Thereafter 1 keeps every record past First.
func BenchmarkMW_SampleKeyed_Kept(b *testing.B) {
	s, err := sample.NewKeyed(discardSink{}, &sample.KeyedConfig{Thereafter: 1, Clock: clock.NewManualClock(time.Unix(1, 0))})
	if err != nil {
		b.Fatalf("sample.NewKeyed err = %v", err)
	}
	runSinkWrite(b, s)
}

// BenchmarkMW_SampleKeyed_Dropped measures the keyed sampler on a record it
// drops: the same lookup, then the drop counter instead of the forward.
func BenchmarkMW_SampleKeyed_Dropped(b *testing.B) {
	s, err := sample.NewKeyed(discardSink{}, &sample.KeyedConfig{First: 1, Thereafter: 1 << 30, Clock: clock.NewManualClock(time.Unix(1, 0))})
	if err != nil {
		b.Fatalf("sample.NewKeyed err = %v", err)
	}
	runSinkWrite(b, s)
}
