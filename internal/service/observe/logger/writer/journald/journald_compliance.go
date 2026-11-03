package journald

import corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"

// journaldSink takes a backlog from the async drainer (#807).
var _ corelogger.BatchSink = (*journaldSink)(nil)
