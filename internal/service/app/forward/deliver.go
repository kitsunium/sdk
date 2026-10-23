package forward

import (
	"context"
	"math"
	"time"

	coreres "github.com/kitsunium/sdk/internal/core/app/resilience"
	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
	svcres "github.com/kitsunium/sdk/internal/service/app/resilience"
)

// deliver is the queue handler: one batch, retried while its failure is
// retryable and its age allows, then acknowledged — sent or expired — or
// dead-lettered. A stop hands it back as it is.
func (f *Forwarder) deliver(ctx context.Context, delivery corequeue.DeliveryValue) error {
	payload := delivery.Message.Payload
	id := delivery.Message.ID
	expiry := delivery.Message.EnqueuedAt.Add(f.cfg.MaxAge)
	//: too old to be worth sending.
	if !f.cfg.Clock.Now().Before(expiry) {
		f.settle(id, &f.stats.Expired)
		//: acknowledged, unsent.
		return nil
	}
	err := f.retry(expiry).Run(ctx, func(attemptCtx context.Context) error {
		//: one attempt, bounded on the clock.
		return f.attempt(attemptCtx, payload)
	})
	//: the verdict.
	switch {
	//: the sender took it.
	case err == nil:
		f.settle(id, &f.stats.Sent)
		//: acknowledged.
		return nil
	//: a stop, or a lease lost: handed back, sent again later.
	case ctx.Err() != nil:
		//: the consumer's to settle.
		return err
	//: refused for good: never sent again.
	case !f.cfg.Retryable(err):
		f.settle(id, &f.stats.DeadLettered)
		//: the broker's Rejecter dead-letters it with the sender's verdict.
		return corequeue.DoNotRetry(err)
	//: still retryable, but past its age.
	default:
		f.settle(id, &f.stats.Expired)
		//: acknowledged, unsent.
		return nil
	}
}

// retry is the policy one batch is retried under: as many attempts as its
// age allows, each failure classified by Config.Retryable, each wait the
// delay the failure imposes or the backoff, on the forwarder's clock.
func (f *Forwarder) retry(expiry time.Time) coreres.Runner {
	//: the SDK's retry, the age as its budget.
	return svcres.NewRetry(svcres.RetryConfig{
		MaxAttempts: math.MaxInt,
		Clock:       f.cfg.Clock,
		Retryable: func(err error) bool {
			//: retryable, and young enough to try again.
			return f.cfg.Retryable(err) && f.cfg.Clock.Now().Before(expiry)
		},
		Delay: func(attempt int, err error) (time.Duration, bool) {
			//: the imposed delay or the curve, never past the age.
			return f.delay(attempt, err, expiry), true
		},
		OnRetry: func(int, error, time.Duration) {
			f.mu.Lock()
			f.stats.Retries++
			f.mu.Unlock()
		},
	})
}

// delay is the wait before the attempt-th retry: what the failure imposes —
// a collector's Retry-After —, recorded, else the backoff curve; either held
// at what is left of the batch's age.
func (f *Forwarder) delay(attempt int, failed error, expiry time.Time) time.Duration {
	left := max(expiry.Sub(f.cfg.Clock.Now()), 0)
	//: the collector said how long.
	if imposed, ok := svcres.RetryAfter(failed); ok {
		f.mu.Lock()
		f.stats.LastRetryAfter = imposed
		f.stats.LastRetryAfterAt = f.cfg.Clock.Now()
		f.mu.Unlock()
		//: honoured in full, but never past the age.
		return min(imposed, left)
	}
	//: the curve.
	return min(f.cfg.Backoff.Delay(attempt), left)
}

// attempt sends payload once, its context cancelled when SendTimeout passes
// on the forwarder's clock.
//
// Goroutine lifecycle: one per attempt waits on the timeout timer. It ends
// when the attempt returns (done closes) or when the timer fires and cancels
// the attempt's context; attempt closes done on every way out.
func (f *Forwarder) attempt(ctx context.Context, payload []byte) error {
	sendCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	timer := f.cfg.Clock.NewTimer(f.cfg.SendTimeout)
	defer timer.Stop()
	done := make(chan struct{})
	defer close(done)
	go func() {
		//: the bound, or the attempt over first.
		select {
		//: the bound passed.
		case <-timer.C():
			cancel()
		//: the attempt returned.
		case <-done:
		}
	}()
	//: the caller's sender.
	return f.cfg.Send(sendCtx, payload)
}
