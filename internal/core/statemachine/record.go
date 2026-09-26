// Package statemachine — hosts the values the Journal port speaks: a record of
// one entity and the steps of its history.
package statemachine

import "time"

// CreateEvent is the event name of the step that brings a new entity into its
// initial state. No declared transition may take it, so a history can always
// tell a creation from an event.
const CreateEvent string = "create"

// RecordValue is what a machine keeps about one entity beside the store: the
// state it is in, since when, and the transitions that brought it there,
// oldest first and bounded by the machine's history length.
//
// State and Entered describe the entity as the machine last saw it. When an
// entity's state changes behind the machine's back — a write that did not go
// through a transition — the machine records the new state with Entered set
// to the moment it learned of it, and adds no step: it knows the state, not a
// transition.
type RecordValue[S comparable] struct {
	// Key is the entity's key in the store.
	Key string `json:"key"`
	// State is the state the entity is in.
	State S `json:"state"`
	// Entered is when the entity entered State.
	Entered time.Time `json:"entered"`
	// History holds the latest transitions, oldest first.
	History []StepValue[S] `json:"history,omitempty"`
}

// StepValue is one transition in a record's history: which, from where to
// where, what fired it, who asked, and when it was stored.
type StepValue[S comparable] struct {
	// Event is the transition's name; [CreateEvent] for a creation.
	Event string `json:"event"`
	// From is the state left: the zero S for a creation.
	From S `json:"from"`
	// To is the state entered.
	To S `json:"to"`
	// Trigger says what fired the transition.
	Trigger Trigger `json:"trigger"`
	// Actor says who fired it when a caller did, as the machine's
	// configuration reads it from the caller's context; empty when the
	// machine's own loop fired it or nobody said.
	Actor string `json:"actor,omitempty"`
	// At is when the transition was stored.
	At time.Time `json:"at"`
}
