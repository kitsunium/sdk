// Package storeengine is what framework/app/store plugs into kit: the engines
// the framework's stores run on — the SDK's document store in memory, in the data
// directory and over SQL —, the framework's scope on a database — the SDK's
// transaction manager, its check and its migrator — and the migrations of
// the framework's tables (ADR 0179 §1). The framework's core imports neither the SDK's docstore
// nor its sql: it reaches them through plug, and a product that declares no
// store links none of them.
//
// Package storeengine — the compile-time proof that the engines are the framework's.
package storeengine
