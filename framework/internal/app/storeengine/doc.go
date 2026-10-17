// Package storeengine is what framework/app/store plugs into kit: the engines
// kit's stores run on — the SDK's document store in memory, in the data
// directory and over SQL —, kit's scope on a database — the SDK's
// transaction manager, its check and its migrator — and the migrations of
// kit's tables (ADR 0179 §1). kit's core imports neither the SDK's docstore
// nor its sql: it reaches them through plug, and a product that declares no
// store links none of them.
//
// Package storeengine — the compile-time proof that the engines are kit's.
package storeengine
