package cache

// Policy chooses which entry a full Cache evicts. The zero value is
// PolicyLRU, today's behaviour; an unknown value runs as PolicyLRU, and
// Stats says which policy runs.
type Policy uint8

const (
	// PolicyLRU evicts the least-recently-used entry: every hit moves its entry
	// to the front. A scan of cold keys longer than the capacity flushes the
	// whole hot set.
	PolicyLRU Policy = iota
	// PolicyS3FIFO is S3-FIFO (Yang et al., SOSP 2023): a new key enters a small
	// probationary FIFO holding a tenth of the capacity; one hit there promotes
	// it to the main FIFO when it reaches the tail, none sends its key to a
	// bounded ghost set, and a key the ghost remembers is admitted straight into
	// main. A one-time scan therefore churns the small queue and leaves the hot
	// set in main. A hit only sets a counter — it moves nothing.
	PolicyS3FIFO
)
