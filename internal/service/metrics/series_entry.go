// Package metrics — one live series.
package metrics

import coremetrics "github.com/kitsunium/sdk/internal/core/metrics"

// seriesEntry binds one instrument instance to the identity it was created
// under. name and attrs are written once at creation and never mutated, which
// is what lets Collect hand the attribute slice straight to the snapshot
// instead of cloning one slice per series per collection.
type seriesEntry[T any] struct {
	name  string
	attrs []coremetrics.AttrValue
	inst  T
}
