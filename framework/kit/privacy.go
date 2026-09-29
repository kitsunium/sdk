// Package kit — privacy: kit's own service for holds and the journal.
package kit

import (
	ikit "github.com/kitsunium/sdk/framework/internal/kit"
)

// Privacy is kit's own service: the legal holds and the privacy journal of
// an app that keeps personal data. kit mounts a copy of it — named
// kit.privacy, a name no service of a product can take — in every app whose
// stores keep personal data or declare a retention, before the product's
// services. Its stores stay in the data directory unless the product places
// them (ADR 0004). A product never mounts it itself.
var Privacy *Service = ikit.Privacy
