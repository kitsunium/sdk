package route_test

import (
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
	"github.com/kitsunium/sdk/internal/service/logger/middleware/route"
)

func TestLevelAtLeast(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		min   level.Level
		got   level.Level
		match bool
	}{
		{"above min matches", level.Warn, level.Error, true},
		{"at min matches", level.Warn, level.Warn, true},
		{"below min misses", level.Warn, level.Info, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := route.LevelAtLeast(tc.min)
			if got := p(corelogger.RecordEvent{Level: tc.got}); got != tc.match {
				t.Errorf("LevelAtLeast(%v)(%v) = %v, want %v", tc.min, tc.got, got, tc.match)
			}
		})
	}
}
