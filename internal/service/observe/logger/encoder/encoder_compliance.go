package encoder

import corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"

// Both encoders render bound attributes once (#1016).
var (
	_ corelogger.PrefixEncoder = (*textEncoder)(nil)
	_ corelogger.PrefixEncoder = (*jsonEncoder)(nil)
)
