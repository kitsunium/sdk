// Package queueengine is what framework/app/queue plugs into kit: the brokers
// the framework's consumers' queues run on — the SDK's queue in memory, in the data
// directory and over SQL, a mailer's outbox on a database sealed in front of
// the SQL one —, the consumer that delivers them, and the migration of a
// queue's table (ADR 0179 §1, step 4). The framework's core imports none of the SDK's
// queue: it reaches it through plug, and a product that declares no topic,
// no subscription, no watch, no queued command and no outbox on a database
// links none of it.
//
// Package queueengine — the compile-time proof that the brokers are the framework's.
package queueengine
