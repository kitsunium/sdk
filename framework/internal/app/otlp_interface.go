package app

// OpenTelemetryConfigurer configures the app's OTLP export: [OTLPEndpoint],
// [OTLPBounds], [OTLPWhenFull], [OTLPMaxAge], [OTLPSendTimeout],
// [OTLPSender].
type OpenTelemetryConfigurer interface {
	openTelemetryConfigure(o *otlpOptions)
}
