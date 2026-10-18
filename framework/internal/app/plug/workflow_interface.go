package plug

import (
	"context"
	"iter"
)

// The workflow subsystem's engine (ADR 0179 §3, step 5), which
// framework/internal/app/workflowengine implements over the SDK's statemachine,
// and the ports it runs on — a workflow's store and its journal —, which the
// core implements.

// WorkflowEngine is one workflow's engine as the framework's core uses it: the
// transitions code fires, what a write of the store tells it, what it
// counts and records, and its loop.
type WorkflowEngine interface {
	// Start enters e in the workflow, in the initial state: its OnEnter
	// hooks run, then it is inserted in the store.
	Start(ctx context.Context, e WorkflowEntityValue) (WorkflowEntityValue, error)
	// Fire moves the entity under key along the event's transition leaving
	// its state.
	Fire(ctx context.Context, key, event string) (WorkflowEntityValue, error)
	// Changed tells the engine the entity under key was written.
	Changed(ctx context.Context, key string) error
	// Deleted tells the engine the entity under key was deleted.
	Deleted(ctx context.Context, key string) error
	// Census counts the entities per state.
	Census() map[string]int
	// Records are what the engine knows of each entity.
	Records() []WorkflowRecordValue
	// Run runs the loop that fires the timers and the guards, until ctx
	// ends.
	Run(ctx context.Context) error
}

// WorkflowStore is the port a workflow's engine reads and writes its
// entities through: the workflow's store.
type WorkflowStore interface {
	// Key is e's key.
	Key(e WorkflowEntityValue) string
	// Get reads the entity under key; false when there is none.
	Get(ctx context.Context, key string) (WorkflowEntityValue, bool, error)
	// Insert writes e unless its key is taken; false when it was.
	Insert(ctx context.Context, e WorkflowEntityValue) (bool, error)
	// Replace writes e over the entity of its key; false when there was
	// none.
	Replace(ctx context.Context, e WorkflowEntityValue) (bool, error)
	// All yields every entity of the store.
	All(ctx context.Context) iter.Seq2[WorkflowEntityValue, error]
}

// WorkflowJournal is the port a workflow's engine keeps what it knows of
// each entity in: the workflow's file under the data directory, or its
// table on the database that keeps its store.
type WorkflowJournal interface {
	// Load reads every record the journal kept.
	Load(ctx context.Context) ([]WorkflowRecordValue, error)
	// Save keeps records, in the transaction ctx carries for a database.
	Save(ctx context.Context, records ...WorkflowRecordValue) error
	// Delete forgets the records of keys; one already gone is no error.
	Delete(ctx context.Context, keys ...string) error
}
