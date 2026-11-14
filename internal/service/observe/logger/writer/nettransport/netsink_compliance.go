package nettransport

import corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"

var (
	// netSink takes a backlog from the async drainer (#807).
	_ corelogger.BatchSink = (*netSink)(nil)
	// netSink says whether it reads RecordEvent.Attrs (#1016).
	_ corelogger.AttrsRequester = (*netSink)(nil)
)
