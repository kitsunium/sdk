package cache

import "time"

// NoExpiry is the [EntryValue.TTL] that stores an entry with no deadline.
//
// It is a distinct value from the zero TTL, which means "use the store's
// default". A cache with a one-minute default holding one entry that must
// never expire is a real combination, and reading both meanings out of 0 would
// make it unspellable — the caller would have to know the default and restate
// it, which stops being true the day the default changes.
const NoExpiry time.Duration = -1
