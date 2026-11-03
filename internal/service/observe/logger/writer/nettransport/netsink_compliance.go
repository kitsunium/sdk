package nettransport

import corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"

// netSink takes a backlog from the async drainer (#807).
var _ corelogger.BatchSink = (*netSink)(nil)
