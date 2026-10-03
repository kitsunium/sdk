// Package recover declares the codes and sentinels of the logger's
// panic-recovering middleware,
// internal/service/observe/logger/middleware/recover: range 0.3.21.*, allocated
// to that engine (ADR 0005 service/observe/logger/middleware/recover block) and
// declared here since ADR 0160, so the engine declares none.
package recover

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.21.0 - 0.3.21.255

// CodeRecoverPanicked identifies a Write call where the wrapped downstream
// sink panicked. The sentinel wraps a fmt.Errorf-style string of the panic
// value so callers can treat panics as ordinary errors.
const CodeRecoverPanicked errs.Code = 0x00_03_15_01 // 0.3.21.1

// CodeRecoverDownstreamNil identifies a New call made with a nil downstream.
const CodeRecoverDownstreamNil errs.Code = 0x00_03_15_02 // 0.3.21.2
