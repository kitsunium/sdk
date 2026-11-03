package file

import corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"

// fileSink takes a whole backlog in one write (#807).
var _ corelogger.BatchSink = (*fileSink)(nil)
