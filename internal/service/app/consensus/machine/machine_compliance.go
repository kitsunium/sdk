package machine

import coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"

// The ports each machine implements: SQL every sibling a durable, batching
// machine speaks, KV the state machine alone (it is volatile).
var (
	_ coreconsensus.DurableStateMachine  = (*SQL)(nil)
	_ coreconsensus.BatchingStateMachine = (*SQL)(nil)
	_ coreconsensus.StateMachine         = (*KV)(nil)
)
