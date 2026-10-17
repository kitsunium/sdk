// Package jobkit is what framework/kit/jobs plugs into kit: the scheduler
// kit's jobs run on — the SDK's scheduler, on the app's clock — and the
// schedules it reads from a job's declaration, a period or a five-field cron
// expression (ADR 0179 §1, step 7). kit's core imports none of the SDK's
// scheduler: it reaches it through plug, and a product that declares no job
// links none of it.
//
// Package jobkit — the compile-time proof that the scheduler is kit's.
package jobkit
