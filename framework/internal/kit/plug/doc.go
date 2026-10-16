// Package plug is where kit's opt-in subsystems plug in: the HTTP engine, the
// frontends' file server and the process sampler of the runtime graph
// (framework/kit/server), the Studio's event stream and its profiler
// (framework/kit/studio). kit reads what they
// registered through the hooks declared here, and imports none of the SDK
// packages behind them: a product that does not import a subsystem neither
// links nor initialises its packages.
//
// It is also the contract between kit's core and the subsystems ADR 0179
// takes out of it, as database/sql/driver is between database/sql and a
// driver: the store subsystem's engine over documents, its siblings and
// kit's scope on a database ([Stores]), which framework/kit/store will set.
// Until it exists the core runs the stores itself; the start refuses a
// declaration whose subsystem the program does not link.
//
// The package imports the standard library and the framework's model only,
// so that kit and every subsystem can import it.
package plug
