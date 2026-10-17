package app

import (
	"github.com/kitsunium/sdk/framework/internal/app/plug"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// kit's workflows through the workflow subsystem (ADR 0179 §1, step 5): the
// core declares every workflow to the engine framework/app/workflow plugs
// in, with a plug.WorkflowConfig — the SDK's statemachine instantiated once,
// over kit's entity and the state's spelling (Q3) —, and holds it as a
// plug.WorkflowEngine; it imports none of the SDK's statemachine.

// workflowPackage is the package a program imports for its workflows.
const workflowPackage = "github.com/kitsunium/sdk/framework/app/workflow"

// workflowsMissing is the refusal of a workflow in a program that does not
// import the workflow subsystem.
func workflowsMissing() error {
	return errs.New(CodeSubsystemMissing, "SUBSYSTEM_NOT_IMPORTED", subsystemPublic,
		"kit: a workflow needs the workflow subsystem, which the program does not import: "+workflowPackage,
		errs.String("packages", workflowPackage))
}

// workflowsLinked reports whether the program imports framework/app/workflow.
func workflowsLinked() bool { return plug.Workflows.Load() != nil }

// workflowFailed reports whether err is the workflow engine's failure of
// kind: the engine's sentinels stay in the subsystem, which tells them
// apart. A workflow runs only with the subsystem, so without it no error is
// one of its failures.
func workflowFailed(err error, kind plug.WorkflowFailure) bool {
	d := plug.Workflows.Load()
	return err != nil && d != nil && d.Failed != nil && d.Failed(err, kind)
}
