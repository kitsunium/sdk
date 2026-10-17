// Package jobengine is what framework/app/jobs plugs into kit: the scheduler
// the framework's jobs run on — the SDK's scheduler, on the app's clock — and the
// schedules it reads from a job's declaration, a period or a five-field cron
// expression (ADR 0179 §1, step 7). The framework's core imports none of the SDK's
// scheduler: it reaches it through plug, and a product that declares no job
// links none of it.
//
// Package jobengine — the compile-time proof that the scheduler is the framework's.
package jobengine
