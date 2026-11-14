package journald

import corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"

var (
	// journaldSink takes a backlog from the async drainer (#807).
	_ corelogger.BatchSink = (*journaldSink)(nil)
	// journaldSink says whether it reads RecordEvent.Attrs (#1016).
	_ corelogger.AttrsRequester = (*journaldSink)(nil)
)
