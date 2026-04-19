// Package codec: codes.go — range 4200-4299 for facade-level errors.
package codec

// range: 4200-4299

// CodeUnknownFormat fires when a Format is not registered in the registry.
const CodeUnknownFormat int = 4201

// CodeCodecUnavailable fires when a codec entry exists but is unusable
// (e.g., blank import missing or build tag not active).
const CodeCodecUnavailable int = 4202

// CodeStreamingUnsupported fires when NewEncoder / NewDecoder is called on
// a codec that does not implement StreamingCodec.
const CodeStreamingUnsupported int = 4203
