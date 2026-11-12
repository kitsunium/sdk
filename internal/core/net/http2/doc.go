// Package http2 is the contract of the SDK's HTTP/2 codec (ADR 0221): the
// values both engines read — the frame header, the settings, the bounds of
// ADR 0195 §D3 as a value — and the sentinels and codes its refusals carry,
// range 0.3.109.*, declared here at the engine's path (ADR 0160). The wire
// format itself, the frame layer and HPACK, is internal/service/net/http2's.
// A wire error code (ErrCode) is the protocol's vocabulary and is not an
// error of the SDK's; a sentinel is what a program branches on.
package http2
