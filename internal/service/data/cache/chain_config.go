package cache

import (
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// ChainConfig parameterises [NewChain]. Its zero value builds a working chain:
// an observation hook, and the clock and step LoadLeased waits on.
type ChainConfig struct {
	// OnPromoteError, if non-nil, is called when a value served from a FAR
	// tier could not be written into a nearer one.
	//
	// A failed promotion is not a failed read: the caller already has the
	// value, and failing the call would turn a degraded cache into a degraded
	// service. But it is also not nothing — a near tier that rejects every
	// promotion is a tier that will never be used again, and with this hook nil
	// that is completely invisible. Wire it.
	OnPromoteError func(key string, err error)
	// Clock is what LoadLeased waits on between two reads while another
	// process holds a key's lease. nil means clock.System. Nothing else in
	// the chain reads time: every tier keeps its own clock.
	Clock clock.Timed
	// LeasePoll is how often LoadLeased reads the chain again while another
	// process holds a key's lease. 0 means 25ms, the file locker's own poll;
	// negative is refused.
	LeasePoll time.Duration
}
