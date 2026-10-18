// Package plug is where the framework's opt-in subsystems plug in: the HTTP engine, the
// frontends' file server and the process sampler of the runtime graph
// (framework/app/server), the Studio — the tool's variables, its routes and
// the framework's sentences in the languages after English (framework/app/studio) —, the
// stores and the databases (framework/app/store), the queues
// (framework/app/queue), the workflows (framework/app/workflow), the mailers
// (framework/app/mail), the jobs (framework/app/jobs). The framework reads
// what they registered through the hooks declared here, and imports none of
// the SDK packages behind them: a product that does not import a subsystem
// neither links nor initialises its packages.
//
// It is also the contract between the framework's core and the subsystems ADR 0179
// takes out of it, as database/sql/driver is between database/sql and a
// driver: the store subsystem's engine over documents, its siblings, the framework's
// scope on a database and its migrations ([Stores]), which framework/app/store
// sets; the refusals an engine says in the framework's terms ([Refusal]); what the framework's
// transactions offer an engine that takes part in them ([Transaction]); the
// queue subsystem's brokers and their consumer ([Queues]), which
// framework/app/queue sets; the workflow subsystem's engine and the ports
// it runs on ([Workflows]), which framework/app/workflow sets; the mail
// subsystem's outbox and transports ([Mail]), which framework/app/mail sets;
// the jobs subsystem's schedules and scheduler ([Jobs]), which
// framework/app/jobs sets; the framework's catalogues in the languages after
// English ([Words]), and the Studio's routes and the tool's variables
// ([Studio], ADR 0180 step 1), which framework/app/studio sets, the routes
// reading the running app through a [StudioHost] the framework gives.
//
// The package imports the standard library and the framework's model only,
// so that the framework and every subsystem can import it.
package plug
