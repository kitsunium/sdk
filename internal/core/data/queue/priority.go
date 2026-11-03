package queue

// Priority is the level a message is delivered at, one of five from
// [PriorityLowest] to [PriorityHighest]; the zero is [PriorityNormal]
// (ADR 0054, amended 2026-11-03).
type Priority int8

// The five levels a message is published at ([PriorityPublisher]), a
// closed set: a Receive hands out the visible messages of the highest
// level first, then by visibility, then by identifier — strict, with no
// ageing, so a lower level is starved for as long as a higher one has a
// visible message.
const (
	// PriorityLowest is the lowest level: received only when no other level has a
	// visible message.
	PriorityLowest Priority = iota - 2
	// PriorityLow is below the normal level: bulk work that may wait behind the
	// normal stream.
	PriorityLow
	// PriorityNormal is the zero Priority, the level [Broker.Publish] publishes at
	// and every message written before priorities existed reads as.
	PriorityNormal
	// PriorityHigh is above the normal level: received before any visible normal
	// message.
	PriorityHigh
	// PriorityHighest is the highest level: received before any other visible
	// message.
	PriorityHighest
)
