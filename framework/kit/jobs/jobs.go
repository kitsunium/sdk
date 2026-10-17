// Package jobs plugs kit's jobs in: the work a service declares on a
// schedule — every period (Service.Every) or on a five-field cron expression
// (Service.Cron) —, which the SDK's scheduler fires on the app's clock. A
// product that declares one imports it:
//
//	import _ "github.com/kitsunium/sdk/framework/kit/jobs"
//
// A product that declares none does not, and links none of that scheduler; a
// program that declares one without it is refused at the start, the refusal
// naming this import (CodeSubsystemMissing). framework/kit imports it too
// until ADR 0179's last step: no product needs the line yet.
package jobs

import "github.com/kitsunium/sdk/framework/internal/kit/jobkit"

// Importing the package plugs the jobs in: a blank package-level value
// rather than an init, the SDK's convention.
var _ = enable()

// enable plugs the jobs' scheduler into every app.
func enable() bool {
	jobkit.Enable()
	return true
}
