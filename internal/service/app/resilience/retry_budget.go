package resilience

import (
	"math"
	"sync/atomic"

	coreres "github.com/kitsunium/sdk/internal/core/app/resilience"
)

// milliPerToken is the fixed-point scale of the budget's counts: gRPC reads
// TokenRatio to three decimals, so a thousandth of a token is the unit and
// every update is one integer compare-and-swap.
const milliPerToken int64 = 1000

// maxBudgetTokens holds MaxTokens where its count in thousandths, doubled by
// AllowRetry's comparison, cannot overflow an int64. It is an int64 because a
// 32-bit int cannot hold it — and cannot reach it either.
const maxBudgetTokens int64 = 1 << 40

// budgetPolicy names a refused budget in PolicyMisconfigured's fields.
const budgetPolicy string = "retrybudget"

// tokenBudget is gRPC's retry-throttling bucket: tokens in thousandths, full
// at max, a failure costing one token, a success returning ratio of one.
type tokenBudget struct {
	tokens atomic.Int64
	max    int64
	ratio  int64
}

// refusedBudget is what NewRetryBudget returns for a configuration it cannot
// honour: it allows no extra attempt, and NewRetry and NewHedge recognise it
// and refuse every call with PolicyMisconfigured naming knob (ADR 0031).
type refusedBudget struct {
	knob string
}

// NewRetryBudget returns the token-bucket RetryBudget of gRPC's retry
// throttling (gRFC A6), to share between the NewRetry and NewHedge policies
// that call one dependency. It starts full, at cfg.MaxTokens tokens; each
// recorded failure costs one token, each recorded success returns
// cfg.TokenRatio of one, never past cfg.MaxTokens; and AllowRetry is true
// only while more than half of cfg.MaxTokens remain. The first attempt of a
// call is never refused, so a dependency that recovers is seen to recover:
// its successes refill the bucket.
//
// The bucket drains while recorded failures exceed TokenRatio times the
// recorded successes — with 0.1, more than one failure per ten successes —
// and retries stop once it has lost half its tokens; it refills, and
// retries resume, when failures fall back below that ratio. 0.1 is the
// order of the per-client retry budget Google's SRE book recommends (10%)
// and of the AWS SDKs' retry quota.
//
// A non-positive cfg.MaxTokens, and a cfg.TokenRatio that is not a finite
// number of at least one thousandth, are refused (ADR 0031): the budget
// returned allows no retry, and NewRetry or NewHedge given it refuses
// every call with PolicyMisconfigured, naming the field. The counts are
// kept in thousandths of a token, as gRPC does, without a lock and without
// allocating.
func NewRetryBudget(cfg RetryBudgetConfig) coreres.RetryBudget {
	//: no bucket at all: there is no size the SDK could pick for the caller.
	if cfg.MaxTokens <= 0 {
		//: refused, and the policies given it say why.
		return refusedBudget{knob: "MaxTokens"}
	}
	//: NaN and ±Inf first — they pass or fail every comparison for the wrong
	//: reason — then the thousandth gRPC reads the ratio to.
	if math.IsNaN(cfg.TokenRatio) || math.IsInf(cfg.TokenRatio, 0) {
		//: refused.
		return refusedBudget{knob: "TokenRatio"}
	}
	//: the ratio in thousandths; a zero would never refill a drained bucket.
	ratio := math.Round(cfg.TokenRatio * float64(milliPerToken))
	//: below a thousandth rounds to nothing.
	if ratio < 1 {
		//: refused.
		return refusedBudget{knob: "TokenRatio"}
	}
	maxMilli := min(int64(cfg.MaxTokens), maxBudgetTokens) * milliPerToken
	//: a success never puts back more than the whole bucket; held in the float
	//: domain before converting, so a huge ratio cannot overflow.
	b := &tokenBudget{max: maxMilli, ratio: int64(min(ratio, float64(maxMilli)))}
	//: the bucket starts full: nothing has failed yet.
	b.tokens.Store(maxMilli)
	//: a lock-free counter — safe to share between policies and goroutines.
	return b
}

// AllowRetry reports whether more than half the tokens remain.
func (b *tokenBudget) AllowRetry() bool {
	//: strictly more than half, as gRPC's "token_count > max_tokens / 2";
	//: doubled rather than halved so an odd count in thousandths is exact.
	return b.tokens.Load()*2 > b.max
}

// RecordSuccess puts ratio back, never past the bucket's size.
func (b *tokenBudget) RecordSuccess() {
	//: compare-and-swap until this success is folded in.
	for {
		current := b.tokens.Load()
		next := min(current+b.ratio, b.max)
		//: already full: nothing to write.
		if next == current || b.tokens.CompareAndSwap(current, next) {
			//: folded in.
			return
		}
	}
}

// RecordFailure takes one token out, never below zero.
func (b *tokenBudget) RecordFailure() {
	//: compare-and-swap until this failure is folded in.
	for {
		current := b.tokens.Load()
		next := max(current-milliPerToken, 0)
		//: already empty: nothing to write.
		if next == current || b.tokens.CompareAndSwap(current, next) {
			//: folded in.
			return
		}
	}
}

// AllowRetry refuses every extra attempt: the budget could not be built.
func (refusedBudget) AllowRetry() bool {
	//: fail closed for a caller that consults it directly.
	return false
}

// RecordSuccess records nothing.
func (refusedBudget) RecordSuccess() {}

// RecordFailure records nothing.
func (refusedBudget) RecordFailure() {}

// refusedBudgetKnob reports whether budget is one NewRetryBudget refused, and
// the field it refused, so a policy given it refuses every call by name.
func refusedBudgetKnob(budget coreres.RetryBudget) (string, bool) {
	refused, ok := budget.(refusedBudget)
	//: the knob is empty when ok is false.
	return refused.knob, ok
}
