package cache

import (
	"context"
	"crypto/rand"
	"encoding/hex"

	corecache "github.com/kitsunium/sdk/internal/core/data/cache"
)

// tokenBytes is the length of a fill lease's token in bytes.
const tokenBytes int = 16

// Load returns the value under key, filling a miss once across the process
// and, with a FillLease, once across the replicas that share the server.
func (s *redisStore[V]) Load(ctx context.Context, key string, fill corecache.Fill[V]) (value V, err error) {
	//: the hit path takes no lock and no lease.
	if hit, found, ferr := s.Fetch(ctx, key); ferr == nil && found {
		//: as stored.
		return hit, nil
	}
	filled, _, gerr := s.group.Do(ctx, key, func(callCtx context.Context) (V, error) {
		//: one fill per key in this process.
		return s.fillShared(callCtx, key, fill)
	})
	//: the shared result, or this caller's own ctx.Err().
	return filled, gerr
}

// fillShared fills key once in this process: directly without a FillLease,
// under the lease otherwise.
func (s *redisStore[V]) fillShared(ctx context.Context, key string, fill corecache.Fill[V]) (value V, err error) {
	//: a caller that queued behind a finished fill finds its value.
	if hit, found, ferr := s.Fetch(ctx, key); ferr == nil && found {
		return hit, nil
	}
	//: the port's own promise: one fill per process.
	if s.fillLease <= 0 {
		return s.fillLocal(ctx, key, fill)
	}
	token := newToken()
	//: until this caller fills, or finds the winner's value.
	for {
		won, aerr := s.client.SetNX(ctx, s.keys.lease(key), token, s.fillLease).Result()
		//: the server failed: nothing is known of the other replicas.
		if aerr != nil {
			var zero V
			//: COMMAND_FAILED.
			return zero, commandFailed(aerr, "SET NX")
		}
		//: this replica fills, and gives the lease back after.
		if won {
			return s.fillLeased(ctx, key, token, fill)
		}
		hit, found, werr := s.awaitWinner(ctx, key)
		//: the winner's value, or a failure.
		if werr != nil || found {
			return hit, werr
		}
	}
}

// fillLeased fills under a lease this replica holds, then releases it.
func (s *redisStore[V]) fillLeased(ctx context.Context, key, token string, fill corecache.Fill[V]) (value V, err error) {
	defer func() {
		//: released only while it is still this caller's: a lease that lapsed
		//: during a slow fill may be another replica's by now.
		_ = releaseScript.Run(context.WithoutCancel(ctx), s.client, []string{s.keys.lease(key)}, token).Err()
	}()
	//: the fill, and its Set.
	return s.fillLocal(ctx, key, fill)
}

// awaitWinner polls every FillPoll for the value the lease holder writes. It
// reports found false when the lease is gone with no value — the holder
// failed or lapsed —, so the caller competes again.
func (s *redisStore[V]) awaitWinner(ctx context.Context, key string) (value V, found bool, err error) {
	//: one poll per FillPoll, on the injected clock.
	for {
		//: every caller of this key has gone.
		if waitErr := s.pollWait(ctx); waitErr != nil {
			var zero V
			//: the caller's end.
			return zero, false, waitErr
		}
		hit, present, ferr := s.Fetch(ctx, key)
		//: the winner's value, or a failure.
		if ferr != nil || present {
			return hit, present, ferr
		}
		held, eerr := s.client.Exists(ctx, s.keys.lease(key)).Result()
		//: the server failed.
		if eerr != nil {
			var zero V
			//: COMMAND_FAILED.
			return zero, false, commandFailed(eerr, "EXISTS")
		}
		//: no lease and no value: compete again.
		if held == 0 {
			return value, false, nil
		}
	}
}

// fillLocal runs fill and stores what it returns.
func (s *redisStore[V]) fillLocal(ctx context.Context, key string, fill corecache.Fill[V]) (value V, err error) {
	entry, ferr := fill(ctx)
	//: a failed fill stores nothing.
	if ferr != nil {
		var zero V
		//: CACHE_FILL_FAILED, or the fill's own SDK code.
		return zero, fillFailure(ferr)
	}
	//: stored for every replica.
	if serr := s.Set(ctx, key, entry); serr != nil {
		var zero V
		//: the Set's refusal.
		return zero, serr
	}
	//: the filled value.
	return entry.Value, nil
}

// newToken is a random lease token: the holder alone releases its lease.
func newToken() string {
	raw := make([]byte, tokenBytes)
	_, _ = rand.Read(raw)
	//: crypto/rand never fails on the platforms Go supports.
	return hex.EncodeToString(raw)
}

// pollWait waits one FillPoll on the injected clock, or until ctx ends, whose
// error it then returns.
func (s *redisStore[V]) pollWait(ctx context.Context) error {
	poll := s.clock.After(s.fillPoll)
	select {
	//: every caller of this key has gone.
	case <-ctx.Done():
		//: the caller's end.
		return ctx.Err()
	//: the next look.
	case <-poll:
		//: time to look again.
		return nil
	}
}
