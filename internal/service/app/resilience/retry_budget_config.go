package resilience

// RetryBudgetConfig parameterises NewRetryBudget, in the terms of gRPC's
// retryThrottling: the bucket's size and what a success puts back. Neither
// has a default; both are refused when unusable (ADR 0031).
type RetryBudgetConfig struct {
	// MaxTokens is the bucket's size and its starting content. Retries are
	// allowed while more than MaxTokens/2 remain, so it is also how many
	// more failures than the successes pay for the budget absorbs before
	// it starts refusing. A non-positive MaxTokens is refused; values above
	// 2^40 are held there.
	MaxTokens int
	// TokenRatio is what a recorded success puts back, in tokens, a
	// failure costing one. It is read to a thousandth, as gRPC reads it: a
	// value that is not finite, or that rounds below 0.001, is refused —
	// its zero would be a budget that, once drained, never refills.
	TokenRatio float64
}
