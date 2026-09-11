// Package health — hosts HandlerConfig, the one knob the three handlers take.
package health

// HandlerConfig parameterises the three handlers. Its zero value serves the
// aggregate and NOTHING else.
type HandlerConfig struct {
	// Detail adds the per-check breakdown to the body.
	//
	// It is off by default because the default reader of a probe body is an
	// orchestrator that only looks at the status code, while the default
	// REACHER of a probe endpoint is more or less anyone on the network. Even
	// with it on, a check contributes its name, its status, its timing and an
	// errs Public — never a raw error, never a Private, never a field.
	Detail bool
}
