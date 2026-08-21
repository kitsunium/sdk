// Package config — the change-observer contract.
package config

import "context"

// Watcher observes a Source for change and invokes onChange (cross-OS poll in
// the default implementation). Watch blocks until ctx is cancelled.
type Watcher interface {
	// Watch calls onChange whenever the underlying source changes, until ctx ends.
	Watch(ctx context.Context, onChange func()) error
}
