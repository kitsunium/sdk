package workflowengine

import (
	"github.com/kitsunium/sdk/framework/internal/app/plug"
	"github.com/kitsunium/sdk/pkg/v1/app/statemachine"
	"github.com/kitsunium/sdk/pkg/v1/clock"
)

// : Asserts at compile time that a workflow's engine is what plug
// describes, that the store the core hands is the SDK's engine's port as it
// is — the same methods over kit's entity —, that the journal it hands is,
// once converted, and that the app's clock is the SDK's.
var (
	_ plug.WorkflowEngine = (*engine)(nil)

	_ statemachine.Store[entity]   = plug.WorkflowStore(nil)
	_ statemachine.Journal[string] = journal{}

	_ clock.Timed = sdkClock{}
)
