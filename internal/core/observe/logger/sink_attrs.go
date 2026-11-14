package logger

// SinkWantsAttrs reports s's answer to AttrsRequester: true when s does not
// implement it, so a Sink that says nothing is handed the merged
// attributes, as before. A decorator answers with it for the Sink it
// wraps, and a fan-out with the OR of its branches.
func SinkWantsAttrs(s Sink) bool {
	ar, ok := s.(AttrsRequester)
	//: a Sink that does not say reads them, as every Sink did before the port.
	return !ok || ar.WantsAttrs()
}
