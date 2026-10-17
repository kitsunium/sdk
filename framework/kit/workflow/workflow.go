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
// a workflow runs over a store. framework/kit imports it too until ADR
// 0179's last step: no product needs the line yet.
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
