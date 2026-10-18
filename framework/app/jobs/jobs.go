// Package jobs plugs kit's jobs in: the work a service declares on a
// schedule — every period (Service.Every) or on a five-field cron expression
// (Service.Cron) —, which the SDK's scheduler fires on the app's clock. A
// product that declares one imports it:
//
//	import _ "github.com/kitsunium/sdk/framework/app/jobs"
//
// A product that declares none does not, and links none of that scheduler; a
// program that declares one without it is refused at the start, the refusal
// naming this import (CodeSubsystemMissing). framework/app does not import
// it (ADR 0179 step 10): kit gen writes the line for a designed product, and
// a hand-written one writes it.
package jobs

import "github.com/kitsunium/sdk/framework/internal/app/jobengine"

// Importing the package plugs the jobs in: a blank package-level value
// rather than an init, the SDK's convention.
var _ = enable()

// enable plugs the jobs' scheduler into every app.
func enable() bool {
	jobengine.Enable()
	return true
}
