package trace

import "github.com/kitsunium/sdk/internal/service/observe/internal/otlp"

// OTLPHTTPConfig configures an OTLP/HTTP span exporter. Every field has a
// resolved meaning when left unset except Endpoint, which has none that could be
// right — see ADR 0051 §Decision 5: a full URL used as-is, so a caller writes
// "http://collector:4318" + OTLPTracesPath, and an endpoint that is not an
// absolute http(s) URL with a non-root path is REFUSED at construction
// (OTLPEndpointInvalid). Timeout clamps to DefaultOTLPTimeout and is ignored
// when Client is set; MaxResponseBytes clamps to DefaultOTLPMaxResponseBytes.
//
// It is a DEFINED type over the configuration of the transport both signals
// share (internal/service/observe/internal/otlp.HTTPConfig, where each field is
// documented once), not an alias of it: trace.OTLPHTTPConfig and
// metrics.OTLPHTTPConfig stay two types a caller cannot hand to the wrong
// signal's constructor.
type OTLPHTTPConfig otlp.HTTPConfig
