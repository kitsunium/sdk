package route

import (
	corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"
	"github.com/kitsunium/sdk/internal/core/observe/logger/level"
)

// LevelAtLeast returns a Predicate matching records at or above min.
func LevelAtLeast(min level.Level) Predicate {
	//: closure binds min so the route table stays declarative at the call site.
	return func(r corelogger.RecordEvent) (match bool) {
		//: standard >= comparison; level.Level supports it natively.
		return r.Level >= min
	}
}
