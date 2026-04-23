// Package codec: codes.go — range 1.2.0.* (ADR 0005 pkg/v1/codec block).
package codec

// range: 1.2.0.0 - 1.2.0.255

// CodeUnknownFormat fires when a Format is not registered in the registry.
const CodeUnknownFormat = 0x01_02_00_01 // 1.2.0.1

// CodeCodecUnavailable fires when a codec entry exists but is unusable
// (e.g., blank import missing or build tag not active).
const CodeCodecUnavailable = 0x01_02_00_02 // 1.2.0.2

// CodeStreamingUnsupported fires when NewEncoder / NewDecoder is called on
// a codec that does not implement StreamingCodec.
const CodeStreamingUnsupported = 0x01_02_00_03 // 1.2.0.3
