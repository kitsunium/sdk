package levelgate

import corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"

var (
	// gateSink takes a batch from the async drainer and passes it on (#807).
	_ corelogger.BatchSink = (*gateSink)(nil)
	// gateSink says whether it reads RecordEvent.Attrs (#1016).
	_ corelogger.AttrsRequester = (*gateSink)(nil)
)
