package app

// : Asserts at compile time that a port's breaker is what its start, its
// readiness, its marks and its command reach, and that Timeout's option
// configures an endpoint, an exposure, a command, a query and a port alike.
var (
	_ portBreaker          = (*breakerGuard)(nil)
	_ OperationOption      = (*timeoutOption)(nil)
	_ EndpointConfigurer   = (*timeoutOption)(nil)
	_ ExposeConfigurer     = (*timeoutOption)(nil)
	_ CommandConfigurer    = (*timeoutOption)(nil)
	_ QueryConfigurer      = (*timeoutOption)(nil)
	_ PortPolicyConfigurer = (*timeoutOption)(nil)
)
