package workflowengine

import (
	"context"

	"github.com/kitsunium/sdk/framework/internal/app/plug"
	"github.com/kitsunium/sdk/pkg/v1/app/statemachine"
)

// engine is the SDK's engine as kit's core uses it: its one instantiation,
// at kit's entity and the state's spelling.
type engine struct {
	m *statemachine.Machine[entity, string]
}

// Start enters e in the workflow.
func (x *engine) Start(ctx context.Context, e entity) (entity, error) { return x.m.Start(ctx, e) }

// Fire moves the entity under key along event's transition.
func (x *engine) Fire(ctx context.Context, key, event string) (entity, error) {
	return x.m.Fire(ctx, key, event)
}

// Changed tells the engine the entity under key was written.
func (x *engine) Changed(ctx context.Context, key string) error { return x.m.Changed(ctx, key) }

// Deleted tells the engine the entity under key was deleted.
func (x *engine) Deleted(ctx context.Context, key string) error { return x.m.Deleted(ctx, key) }

// Census counts the entities per state.
func (x *engine) Census() map[string]int { return x.m.Census() }

// Records are what the engine knows of each entity, as plug spells them.
func (x *engine) Records() []plug.WorkflowRecordValue {
	records := x.m.Records()
	out := make([]plug.WorkflowRecordValue, len(records))
	for i := range records {
		out[i] = recordOf(&records[i])
	}
	return out
}

// Run runs the engine's loop until ctx ends.
func (x *engine) Run(ctx context.Context) error { return x.m.Run(ctx) }

// journal is a workflow's journal, which the core keeps, as the SDK's
// engine reads and writes it.
type journal struct {
	j plug.WorkflowJournal
}

// Load reads every record the journal kept.
func (j journal) Load(ctx context.Context) ([]statemachine.Record[string], error) {
	records, err := j.j.Load(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]statemachine.Record[string], len(records))
	for i := range records {
		out[i] = sdkRecordOf(&records[i])
	}
	return out, nil
}

// Save keeps records.
func (j journal) Save(ctx context.Context, records ...statemachine.Record[string]) error {
	out := make([]plug.WorkflowRecordValue, len(records))
	for i := range records {
		out[i] = recordOf(&records[i])
	}
	return j.j.Save(ctx, out...)
}

// Delete forgets the records of keys.
func (j journal) Delete(ctx context.Context, keys ...string) error { return j.j.Delete(ctx, keys...) }

// recordOf is the engine's record as plug spells it.
func recordOf(r *statemachine.Record[string]) plug.WorkflowRecordValue {
	out := plug.WorkflowRecordValue{Key: r.Key, State: r.State, Entered: r.Entered}
	for _, s := range r.History {
		out.History = append(out.History, plug.WorkflowStepValue{
			Event: s.Event, From: s.From, To: s.To, Trigger: triggerOf(s.Trigger), Actor: s.Actor, At: s.At,
		})
	}
	return out
}

// sdkRecordOf is plug's record as the engine reads it.
func sdkRecordOf(r *plug.WorkflowRecordValue) statemachine.Record[string] {
	out := statemachine.Record[string]{Key: r.Key, State: r.State, Entered: r.Entered}
	for _, s := range r.History {
		out.History = append(out.History, statemachine.Step[string]{
			Event: s.Event, From: s.From, To: s.To, Trigger: sdkTriggerOf(s.Trigger), Actor: s.Actor, At: s.At,
		})
	}
	return out
}

// changeOf is the engine's stored transition as plug spells it.
func changeOf(c *statemachine.Change[entity, string]) plug.WorkflowChangeValue {
	return plug.WorkflowChangeValue{
		Key: c.Key, Entity: c.Entity, Event: c.Event, From: c.From, To: c.To,
		Trigger: triggerOf(c.Trigger), Actor: c.Actor, At: c.At,
	}
}

// observeOf is kit's bracket around each transition the loop fires, as
// the engine calls it; nil for none.
func observeOf(fn func(context.Context, plug.WorkflowFiringValue) (context.Context, func(error))) func(context.Context, statemachine.Firing[string]) (context.Context, func(error)) {
	if fn == nil {
		return nil
	}
	return func(ctx context.Context, f statemachine.Firing[string]) (context.Context, func(error)) {
		return fn(ctx, plug.WorkflowFiringValue{Key: f.Key, Event: f.Event, From: f.From, To: f.To, Trigger: triggerOf(f.Trigger)})
	}
}

// onLoopOf is kit's view of the loop, as the engine tells it; nil for
// none.
func onLoopOf(fn func(plug.WorkflowLoopEventValue)) func(statemachine.LoopEvent) {
	if fn == nil {
		return nil
	}
	return func(e statemachine.LoopEvent) {
		fn(plug.WorkflowLoopEventValue{
			Err: e.Err, Started: e.Started, Ended: e.Ended, Next: e.Next, Kind: loopKindOf(e.Kind), Wake: wakeOf(e.Wake),
		})
	}
}

// triggerOf is the engine's trigger as plug says it.
func triggerOf(t statemachine.Trigger) plug.WorkflowTrigger {
	switch t {
	case statemachine.TriggerStart:
		return plug.WorkflowTriggerStart
	case statemachine.TriggerDelay:
		return plug.WorkflowTriggerDelay
	case statemachine.TriggerDeadline:
		return plug.WorkflowTriggerDeadline
	case statemachine.TriggerGuard:
		return plug.WorkflowTriggerGuard
	}
	return plug.WorkflowTriggerEvent
}

// sdkTriggerOf is plug's trigger as the engine says it.
func sdkTriggerOf(t plug.WorkflowTrigger) statemachine.Trigger {
	switch t {
	case plug.WorkflowTriggerStart:
		return statemachine.TriggerStart
	case plug.WorkflowTriggerDelay:
		return statemachine.TriggerDelay
	case plug.WorkflowTriggerDeadline:
		return statemachine.TriggerDeadline
	case plug.WorkflowTriggerGuard:
		return statemachine.TriggerGuard
	default:
		return statemachine.TriggerEvent
	}
}

// wakeOf is why the engine's loop ran, as plug says it.
func wakeOf(w statemachine.Wake) plug.WorkflowWake {
	switch w {
	case statemachine.WakeDue:
		return plug.WorkflowWakeDue
	case statemachine.WakeChange:
		return plug.WorkflowWakeChange
	}
	return plug.WorkflowWakeStart
}

// loopKindOf is what the engine's loop reports, as plug says it.
func loopKindOf(k statemachine.LoopEventKind) plug.WorkflowLoopEvent {
	switch k {
	case statemachine.LoopRunStarted:
		return plug.WorkflowLoopRunStarted
	case statemachine.LoopRunEnded:
		return plug.WorkflowLoopRunEnded
	case statemachine.LoopWaiting:
		return plug.WorkflowLoopWaiting
	}
	return plug.WorkflowLoopOther
}
