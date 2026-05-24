// Package codec — range 1.2.0.* (ADR 0005 pkg/v1/codec block).
package codec

import "github.com/kitsunium/sdk/pkg/v1/errs"

// range: 1.2.0.0 - 1.2.0.255

// CodeUnknownFormat fires when a Format is not registered in the registry.
const CodeUnknownFormat errs.Code = 0x01_02_00_01 // 1.2.0.1

// CodeCodecUnavailable fires when a codec entry exists but is unusable
// (e.g., blank import missing or build tag not active).
const CodeCodecUnavailable errs.Code = 0x01_02_00_02 // 1.2.0.2

// CodeStreamingUnsupported fires when NewEncoder / NewDecoder is called on
// a codec that does not implement StreamingCodec.
const CodeStreamingUnsupported errs.Code = 0x01_02_00_03 // 1.2.0.3

// CodePromoteFailed fires when the facade's JSON-bridge promotion path
// cannot serve the request (unknown Format with no promotion strategy,
// or a malformed container after the codec populated it).
const CodePromoteFailed errs.Code = 0x01_02_00_04 // 1.2.0.4
