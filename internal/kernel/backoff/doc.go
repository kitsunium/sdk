// Package backoff computes an exponential backoff: how long a loop that keeps
// failing waits before its next attempt. It is a kernel primitive — stdlib-only
// and domain-neutral: a duration, a growth factor, a ceiling and a jitter, with
// no Retry, no Job and no Delivery in a signature.
//
// One curve serves every loop that backs off. The retry policy waits it between
// attempts; a supervised goroutine waits it between restarts, an outbox between
// deliveries, a state machine between failed transitions, a server between
// failed accepts. Each of those used to compute its own copy, and the copy that
// lived in the retry policy carried a defect the shared one fixes (ADR 0103):
// see [Value] for the float-to-duration conversion that made amd64 retries stop
// waiting at attempt 35.
//
// The curve is two claims kept apart on purpose. [Grow] is the deterministic
// growth, a PURE function of the attempt; [Widen] is the randomisation laid on
// top of it. [Value.Delay] composes the two. A suite that cannot assert the
// growth without the jitter can assert neither precisely, and a caller that
// wants the deterministic curve asks for it by name.
package backoff
