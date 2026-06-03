package nettransport

import (
	"context"
	"sync"
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
)

func Test_compose(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		minLevel level.Level
		level    level.Level
		want     int
	}{
		{"record at floor reaches the seam", level.Info, level.Error, 1},
		{"below-floor record is dropped", level.Error, level.Debug, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var mu sync.Mutex
			count := 0
			send := func(context.Context, []byte) error {
				mu.Lock()
				count++
				mu.Unlock()
				return nil
			}
			sink := compose("tcp", send, func() error { return nil }, NetConfig{MinLevel: tc.minLevel})
			//: compose must return a usable chain.
			if sink == nil {
				t.Fatal("compose returned nil sink")
			}
			//: write one record through levelgate(async(netSink)).
			if _, err := sink.Write(t.Context(), corelogger.RecordEvent{Level: tc.level}, []byte("x")); err != nil {
				t.Fatalf("Write: %v", err)
			}
			//: Close joins the async drainer, so the seam has run by now.
			if cerr := sink.Close(); cerr != nil {
				t.Fatalf("Close: %v", cerr)
			}
			mu.Lock()
			got := count
			mu.Unlock()
			//: the record must have reached the terminal send exactly once.
			if got != tc.want {
				t.Errorf("%s: send count = %d, want %d", tc.name, got, tc.want)
			}
		})
	}
}
