package events

// PriorityNormal is the zero [Priority] and the value a caller with no
// opinion about ordering should leave in place. Negative priorities run
// before it, positive ones after.
const PriorityNormal Priority = 0
