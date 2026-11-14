package file

import corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"

var (
	// fileSink takes a whole backlog in one write (#807).
	_ corelogger.BatchSink = (*fileSink)(nil)
	// fileSink says whether it reads RecordEvent.Attrs (#1016).
	_ corelogger.AttrsRequester = (*fileSink)(nil)
)
