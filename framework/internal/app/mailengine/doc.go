// Package mailengine is what framework/app/mail plugs into kit: the outbox
// kit's mailers keep — the SDK's mail spool, on the database that keeps the
// mailer's service in the queue subsystem's sealed broker, else in the data
// directory or in memory — and the transports that empty it, the SDK's SMTP
// transport and its capture transport (ADR 0179 §1, step 6). kit's core
// imports neither the SDK's spool nor a transport: it reaches them through
// plug, and a product that declares no mailer links none of them.
//
// Package mailengine — the compile-time proof that the outbox is kit's.
package mailengine
