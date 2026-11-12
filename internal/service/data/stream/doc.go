// Package stream holds the engines of core/data/stream's [corestream.Stream] port —
// memory, a directory of segments over kernel/fs/seglog, and a SQL table
// joined to the caller's transaction — and the subscriber that reads a stream
// on behalf of a consumer group (ADR 0231). The engines arrive one per
// milestone of kitsunium/sdk#890; their error codes, 0.3.94.*, are already
// declared in the core at this path (ADR 0160).
package stream
