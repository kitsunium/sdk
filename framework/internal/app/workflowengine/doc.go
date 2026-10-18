// Package workflowengine is what framework/app/workflow plugs into kit: the
// engine the framework's workflows run on — the SDK's statemachine, instantiated once,
// over the framework's entity and the state's spelling (ADR 0179 §1, step 5, Q3) —,
// its loop of timers and guards, and the conversions between the engine's
// values and plug's. The framework's core imports none of the SDK's statemachine: it
// reaches it through plug, and a product that declares no workflow links
// none of it.
//
// Package workflowengine — the compile-time proof that the engine is the framework's.
package workflowengine
