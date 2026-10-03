// Package memory — compile-time proof that *Memory satisfies the core Sink port.
package memory

import corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"

// _ asserts at compile time that *Memory implements the core Sink interface.
var _ corelogger.Sink = (*Memory)(nil)
