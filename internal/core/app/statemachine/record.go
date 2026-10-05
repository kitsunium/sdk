package statemachine

// CreateEvent is the event name of the step that brings a new entity into its
// initial state. No declared transition may take it, so a history can always
// tell a creation from an event.
const CreateEvent string = "create"
