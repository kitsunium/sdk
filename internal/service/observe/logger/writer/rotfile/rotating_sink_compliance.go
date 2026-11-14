package rotfile

import corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"

var (
	// rotatingSink takes a whole backlog in one write (#807).
	_ corelogger.BatchSink = (*rotatingSink)(nil)
	// rotatingSink says whether it reads RecordEvent.Attrs (#1016).
	_ corelogger.AttrsRequester = (*rotatingSink)(nil)
)
