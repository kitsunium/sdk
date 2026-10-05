package trace

// isEmpty is SpansValue.IsEmpty's body: decl_gen.go writes SpansValue.IsEmpty, from the
// design, as one call of it.
func (s SpansValue) isEmpty() bool {
	//: resource and scope alone are not telemetry.
	return len(s.Spans) == 0
}
