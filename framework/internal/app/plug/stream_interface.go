package plug

import "context"

// The stream subsystem's log (issue #826), which
// framework/internal/app/streamengine implements over the SDK's stream.

// Stream is one stream as the framework's core uses it: an append, and the
// consumer groups that read it.
type Stream interface {
	// Append puts payload in the stream under key, one record: in the
	// transaction ctx carries for its database, when it lives on one.
	Append(ctx context.Context, key string, payload []byte) error
	// Join places the consumer group cfg names where cfg starts it, on every
	// partition the stream does not know it on yet; a group it knows keeps
	// its offsets. The framework joins at the subscription's start, before
	// anything is published, so that a group starting now misses nothing.
	Join(ctx context.Context, cfg StreamSubscribeConfig) error
	// Subscribe runs the consumer group cfg names over every partition
	// until ctx ends or the stream's storage fails, joining it first.
	Subscribe(ctx context.Context, cfg StreamSubscribeConfig) error
	// Close releases the stream: a file stream's directory, which one
	// engine opens at a time.
	Close() error
}
