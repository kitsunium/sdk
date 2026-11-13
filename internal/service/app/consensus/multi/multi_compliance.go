package multi

import coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"

// Machine is the state machine of a node, applying in batches.
var _ coreconsensus.BatchingStateMachine = (*Machine)(nil)
