package group

import (
	kgroup "github.com/kitsunium/sdk/internal/kernel/concur/group"
)

// Unlimited is the concurrency limit that bounds nothing: a slot is always
// free, so [Group].Go never waits for one. It has to be written out — a zero
// limit is clamped to one, never read as "no limit".
const Unlimited int = kgroup.Unlimited
