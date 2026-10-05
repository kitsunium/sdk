package trace

// IsEmpty reports whether the payload carries no span. An exporter uses it to
// skip a POST that would carry nothing.
func (s SpansValue) IsEmpty() bool {
	//: resource and scope alone are not telemetry.
	return len(s.Spans) == 0
}
