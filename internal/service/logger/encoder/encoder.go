// Package encoder provides concrete Encoder implementations (Text today;
// NDJSON and JSON to land in follow-up commits). The Encoder interface
// itself lives in internal/core/logger — this package is implementation-
// only, exposing the interface via a type alias so existing call sites
// that import "service/logger/encoder".Encoder keep compiling.
//
// Per ADR 0005 hexagonal layering: core/ holds ports, service/ holds
// adapters. Previously Encoder lived here, creating an asymmetry where
// Sink lived in core/ and Encoder lived in service/ even though both are
// ports of the Handler.
package encoder

import corelogger "github.com/kitsunium/sdk/internal/core/logger"

// Encoder is the format-side port now rooted in internal/core/logger.
// Kept as an alias here so consumers that import service/logger/encoder
// for the interface type keep compiling. New code should import the
// canonical type from core/logger directly.
type Encoder = corelogger.Encoder
