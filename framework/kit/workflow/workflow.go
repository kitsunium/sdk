// Package workflow plugs kit's workflows in: the state machines a service
// declares over the entities of its stores — their event, timer and guard
// transitions, their hooks and their loop —, which the SDK's statemachine
// runs. A product that declares one imports it:
//
//	import _ "github.com/kitsunium/sdk/framework/kit/workflow"
//
// A product that declares none does not, and links none of that engine; a
// program that declares one without it is refused at the start, the refusal
// naming this import (CodeSubsystemMissing). It imports framework/kit/store:
// a workflow runs over a store. framework/kit does not import it (ADR 0179
// step 10): kit gen writes the line for a designed product, and a
// hand-written one writes it.
package workflow

import (
	"github.com/kitsunium/sdk/framework/internal/kit/workflowkit"
	// A workflow runs over a store (ADR 0179 Q4).
	_ "github.com/kitsunium/sdk/framework/kit/store"
)

// Importing the package plugs the workflows in: a blank package-level value
// rather than an init, the SDK's convention.
var _ = enable()

// enable plugs the workflows' engine into every app.
func enable() bool {
	workflowkit.Enable()
	return true
}
