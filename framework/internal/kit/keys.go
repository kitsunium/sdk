package kit

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/app/lock"
	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/observe/logger"
)

// A command's key names the entity it is about: two runs of the command with
// one key never overlap, runs with different keys go at once. It is the
// SDK's lock, in the process: across a restart, or between processes, one
// key can run twice — a handler stays idempotent.

// Leases on a key expire, so that a leaked run cannot hold its key for the
// life of the process; a run renews its lease while it lasts, and its context
// ends if the lease is lost.
const (
	keyTTL   time.Duration = 30 * time.Second
	keyRenew time.Duration = keyTTL / 3
)

// keyLocks is a command's locker in one app, on the app's clock.
type keyLocks struct {
	app    *App
	locker lock.Locker
}

// keyed holds a command's key while run runs: it waits for the key, within
// ctx, and refuses a run that already holds it — a handler dispatching its
// own command with its own key would wait for itself.
type keyed struct {
	locks atomic.Pointer[keyLocks]
}

// heldKey is one key a context's run holds, and those its callers hold.
type heldKey struct {
	cmd  node
	key  string
	next *heldKey
}

// heldKeys carries the keys a run holds, innermost first.
type heldKeys struct{}

// holds reports whether ctx runs under cmd's key.
func holds(ctx context.Context, cmd node, key string) bool {
	for h, _ := ctx.Value(heldKeys{}).(*heldKey); h != nil; h = h.next {
		if h.cmd == cmd && h.key == key {
			return true
		}
	}
	return false
}

// reentrant is the refusal of a run that would wait for itself.
func reentrant(cmd node) error {
	return failure(CodeCommandReentrant, "COMMAND_REENTRANT", "a handler dispatched its own command with its own key", nil,
		errs.String("command", cmd.base().id))
}

// run runs fn while cmd's key is held in a.
func (k *keyed) run(ctx context.Context, a *App, cmd node, key string, fn func(context.Context) error) error {
	if holds(ctx, cmd, key) {
		return reentrant(cmd)
	}
	locker, err := k.lockerOf(a)
	if err != nil {
		return err
	}
	lease, err := locker.Acquire(ctx, key)
	if err != nil {
		return keyFailure(ctx, cmd, err)
	}
	defer func() {
		if err := lease.Release(context.WithoutCancel(ctx)); err != nil {
			logger.Warn(ctx, a.log, "a key's lease could not be released: it expires on its own", logger.String("error", errs.PublicOf(err)))
		}
	}()
	guarded, stop, err := lock.Keepalive(ctx, lease, lock.KeepaliveConfig{Every: keyRenew, Clock: a.clock})
	if err != nil {
		return keyFailure(ctx, cmd, err)
	}
	defer stop()
	return fn(context.WithValue(guarded, heldKeys{}, &heldKey{cmd: cmd, key: key, next: heldKeysOf(ctx)}))
}

// heldKeysOf is the keys ctx's run holds.
func heldKeysOf(ctx context.Context) *heldKey {
	h, _ := ctx.Value(heldKeys{}).(*heldKey)
	return h
}

// keyFailure is the error of a key that could not be held: its caller's
// deadline or cancellation as they are — 504, 503 —, anything else kit's.
func keyFailure(ctx context.Context, cmd node, err error) error {
	if ctx.Err() != nil && (errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)) {
		return err
	}
	return failure(CodeCommandKey, "COMMAND_KEY", "the command's key could not be held", err, errs.String("command", cmd.base().id))
}

// lockerOf is the command's locker in a: made at its first keyed run, the
// same for every run of the app.
func (k *keyed) lockerOf(a *App) (lock.Locker, error) {
	for {
		cur := k.locks.Load()
		if cur != nil && cur.app == a {
			return cur.locker, nil
		}
		locker, err := lock.NewMemory(lock.MemoryConfig{TTL: keyTTL, Clock: a.clock})
		if err != nil {
			return nil, failure(CodeCommandKey, "COMMAND_KEY", "the command's key could not be held", err)
		}
		if k.locks.CompareAndSwap(cur, &keyLocks{app: a, locker: locker}) {
			return locker, nil
		}
	}
}
