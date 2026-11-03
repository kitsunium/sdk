package levelgate

import corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"

// gateSink takes a batch from the async drainer and passes it on (#807).
var _ corelogger.BatchSink = (*gateSink)(nil)
