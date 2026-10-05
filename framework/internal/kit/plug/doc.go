// Package plug is where kit's opt-in subsystems plug in: the HTTP engine and
// the frontends' file server (framework/kit/server), the Studio's event
// stream and its profiler (framework/kit/studio). kit reads what they
// registered through the hooks declared here, and imports none of the SDK
// packages behind them: a product that does not import a subsystem neither
// links nor initialises its packages.
//
// The package imports the standard library and the framework's model only,
// so that kit and every subsystem can import it.
package plug
