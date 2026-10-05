package git

// degraded is ResolutionValue.Degraded's body: decl_gen.go writes ResolutionValue.Degraded, from the
// design, as one call of it.
func (r *ResolutionValue) degraded() bool {
	//: FullFallback is the authoritative signal; a nil Set merely accompanies it.
	return r.FullFallback
}
