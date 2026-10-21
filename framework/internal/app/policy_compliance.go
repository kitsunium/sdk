package app

// : Asserts at compile time that MaxWait's option configures a rate limit
// and a bulkhead alike.
var (
	_ RateLimitConfigurer = (*waitOption)(nil)
	_ BulkheadConfigurer  = (*waitOption)(nil)
)
