package kit

import (
	ikit "github.com/kitsunium/sdk/framework/internal/kit"
)

// DefaultMaxBody is the largest request body an endpoint reads, unless the
// endpoint says otherwise with [MaxBody].
const DefaultMaxBody int64 = ikit.DefaultMaxBody
