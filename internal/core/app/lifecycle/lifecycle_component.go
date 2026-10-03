// Package lifecycle — hosts ComponentValue, the registration a Lifecycle owns.
package lifecycle

// ComponentValue is one named unit of the application's startup order: what
// brings it up, and what takes it down.
//
// The zero value is not runnable and is refused by Add ([InvalidComponent])
// rather than accepted and silently skipped. Both halves are required: a
// component with nothing to stop is still asked to declare that explicitly,
// because "I forgot the teardown" and "there is no teardown" are the same
// nil, and only one of them is a defect. `func(context.Context) error { return
// nil }` is two seconds of typing and it puts the claim in the diff.
type ComponentValue struct {
	// Name identifies the component in every [TransitionValue] and in every
	// error field. It must be non-empty and unique within one Lifecycle.
	Name string
	// Start brings the component up. It is called in registration order.
	Start Start
	// Stop takes the component down. It is called ONLY if Start returned nil,
	// and in reverse of the order the components started in.
	Stop Stop
}
