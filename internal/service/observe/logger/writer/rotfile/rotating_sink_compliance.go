package rotfile

import corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"

// rotatingSink takes a whole backlog in one write (#807).
var _ corelogger.BatchSink = (*rotatingSink)(nil)
