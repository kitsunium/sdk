// Package secret — the rotator: a policy that mints new versions of one
// secret on a schedule, keeping enough old ones that nothing sealed before a
// rotation breaks because of it.
package secret

import (
	"context"
	"crypto/rand"
	"errors"
	"sync"
	"time"

	corelock "github.com/kitsunium/sdk/internal/core/lock"
	coresecret "github.com/kitsunium/sdk/internal/core/secret"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// minKeep is the fewest versions a rotation may keep: the new one and the one
// it replaced. Keeping one would retire the previous key at the instant of the
// rotation, so every box sealed a moment before it would stop opening — the
// outage a rotation policy exists to avoid.
const minKeep int = 2

// minRandomBytes is the shortest secret [Random] generates: 128 bits. Anything
// shorter is guessable in a way no caller asking for a secret means.
const minRandomBytes int = 16

// rotateLockPrefix namespaces the lock a rotator takes when it is given one.
const rotateLockPrefix string = "kitsunium/secret-rotate/"

// PolicySpec says how often a secret rotates, how many versions survive a
// rotation, and how a new version is made. Every field is required; NewRotator
// refuses the zero value of each, because none of them has a default the SDK
// could choose on a caller's behalf (ADR 0031). pkg/v1/secret aliases it as
// Policy.
type PolicySpec struct {
	// Every is the rotation interval, measured from the current version's
	// Created. It must be positive.
	Every time.Duration
	// Keep is how many versions a rotation leaves, the new one included. It
	// must be at least 2, so the version a rotation replaces still opens what
	// it sealed; set it to cover the longest-lived box or signature in
	// flight, divided by Every, plus one.
	Keep int
	// Generate makes a new version. [Random] is the generator for keys and
	// tokens. An error or an empty value aborts the rotation with
	// [GenerateFailed]; nothing is stored and nothing is pruned.
	Generate func() (coresecret.Value, error)
}

// Random returns a generator of n bytes from crypto/rand — the one a Keyring's
// versions need with n = 32. An n below 16 bytes yields a generator that
// refuses every call with [InvalidConfig], so the mistake surfaces at the
// first Ensure rather than as a guessable secret.
func Random(n int) func() (coresecret.Value, error) {
	//: the generator a Policy calls on every rotation.
	return func() (coresecret.Value, error) {
		//: a secret shorter than 128 bits is refused, never padded.
		if n < minRandomBytes {
			//: InvalidConfig, naming the setting and the bound.
			return coresecret.Value{}, wrapAs(InvalidConfig, nil,
				errs.String("setting", "Random"), errs.Int("bytes", n), errs.Int("minimum", minRandomBytes))
		}
		raw := make([]byte, n)
		defer clear(raw)
		//: crypto/rand never returns a short read; its error is reported.
		if _, readErr := rand.Read(raw); readErr != nil {
			//: GenerateFailed, with the entropy source's message.
			return coresecret.Value{}, wrapAs(GenerateFailed, readErr)
		}
		//: NewValue copies, so the buffer can be cleared.
		return coresecret.NewValue(raw), nil
	}
}

// RotatorConfig parameterises [NewRotator]: the secret to rotate, where it
// lives, the policy, and the three optional collaborators — a clock, a
// cross-process lock, and a callback told of every rotation.
type RotatorConfig struct {
	// Store holds the secret. Required. A read-only store is accepted and
	// every write it refuses surfaces as core/secret.ReadOnly.
	Store coresecret.Store
	// Name is the secret to rotate. Required.
	Name string
	// Policy is the schedule, the retention and the generator. Required.
	Policy PolicySpec
	// Clock decides when a rotation is due and paces Run. nil means
	// clock.System. A test drives the rotator with a clock.ManualClock — the
	// same one its store stamps Created with, so both read one timeline.
	Clock clock.Timed
	// Locker, when set, serialises rotations of this secret ACROSS PROCESSES:
	// Ensure, Rotate and RotateIfDue take its lock, re-read the current
	// version, and only then decide. Without it, two replicas sharing a file
	// store can both find a rotation due and both perform it — nothing breaks,
	// since both versions are kept keys, but a rotation is wasted and Keep
	// counts it. One process needs no Locker: the rotator serialises its own
	// calls.
	Locker corelock.Locker
	// OnRotate, when set, is told of every rotation this rotator performs,
	// with the version it created. It runs on the rotating goroutine after
	// the version is stored, the old ones pruned and every lock released, so
	// it may call back into the rotator. Ensure creating the first version is
	// not a rotation and does not call it.
	OnRotate func(rotated coresecret.VersionValue)
}

// Rotator mints new versions of one secret according to a [PolicySpec].
//
// It starts no goroutine. [Rotator.RotateIfDue] is one decision a caller can
// drive from anything — a scheduler, a request, a start-up hook — and
// [Rotator.Run] is the loop for a caller that wants one: it blocks on the
// caller's goroutine, sleeps on the injected clock until the next rotation is
// due, and returns when its context ends, so the caller that started it is the
// one that joins it.
type Rotator struct {
	// store holds the secret.
	store coresecret.Store
	// name is the secret.
	name string
	// policy is validated.
	policy PolicySpec
	// clk decides and waits.
	clk clock.Timed
	// locker is nil unless cross-process serialisation was asked for.
	locker corelock.Locker
	// onRotate is nil unless a caller wants to be told.
	onRotate func(rotated coresecret.VersionValue)
	// mu serialises this rotator's own decisions, so two goroutines of one
	// process cannot both rotate one due secret.
	mu sync.Mutex
}

// NewRotator validates cfg and returns a rotator. It refuses a nil store, a
// malformed name, a non-positive interval, fewer than two kept versions and a
// nil generator — each with [InvalidConfig] naming the setting — and touches
// nothing: the secret is created by Ensure, not here.
func NewRotator(cfg RotatorConfig) (rotator *Rotator, err error) {
	//: every refusal is decided before anything is built.
	if invalid := cfg.validate(); invalid != nil {
		//: InvalidConfig or InvalidName.
		return nil, invalid
	}
	clk := cfg.Clock
	//: nil is the production default.
	if clk == nil {
		//: the system time source.
		clk = clock.System
	}
	//: a rotator that has not yet looked at the store.
	return &Rotator{
		store:    cfg.Store,
		name:     cfg.Name,
		policy:   cfg.Policy,
		clk:      clk,
		locker:   cfg.Locker,
		onRotate: cfg.OnRotate,
	}, nil
}

// validate refuses a configuration no rotator could honour.
func (c RotatorConfig) validate() error {
	//: nowhere to rotate.
	if c.Store == nil {
		//: InvalidConfig, naming the setting.
		return refuseSetting("Store", "nil")
	}
	//: the name every call will use.
	if nameErr := coresecret.ValidateName(c.Name); nameErr != nil {
		//: InvalidName.
		return nameErr
	}
	//: an interval of zero would rotate on every call.
	if c.Policy.Every <= 0 {
		//: InvalidConfig.
		return refuseSetting("Policy.Every", "must be positive")
	}
	//: keeping one would retire the previous key at the rotation itself.
	if c.Policy.Keep < minKeep {
		//: InvalidConfig.
		return refuseSetting("Policy.Keep", "must be at least 2")
	}
	//: no way to make a new version.
	if c.Policy.Generate == nil {
		//: InvalidConfig.
		return refuseSetting("Policy.Generate", "nil")
	}
	//: a rotator that can work.
	return nil
}

// refuseSetting is the InvalidConfig verdict for one setting.
func refuseSetting(setting, problem string) error {
	//: the setting and the clause; never a value.
	return wrapAs(InvalidConfig, nil, errs.String("setting", setting), errs.String("problem", problem))
}

// Ensure returns the current version, creating version 1 with the policy's
// generator when the secret has none. It is the call to make at start-up,
// before a Keyring over the same name is asked to seal. Creating the first
// version is not a rotation, so OnRotate is not told.
func (r *Rotator) Ensure(ctx context.Context) (current coresecret.VersionValue, err error) {
	serialErr := r.serialised(ctx, func() error {
		found, findErr := r.currentOrFirst(ctx)
		current = found
		//: the store's verdict, or the version found or created.
		return findErr
	})
	//: the lock or the work failed.
	if serialErr != nil {
		//: no version is reported alongside a failure.
		return coresecret.VersionValue{}, serialErr
	}
	//: the current version, possibly just created.
	return current, nil
}

// Due reports when the current version becomes due for rotation: its Created
// plus the policy's interval. It returns core/secret.NotFound when the secret
// has no version yet — Ensure creates it.
func (r *Rotator) Due(ctx context.Context) (due time.Time, err error) {
	current, getErr := r.store.Get(ctx, r.name)
	//: no version, or no store to ask.
	if getErr != nil {
		//: the store's verdict.
		return time.Time{}, getErr
	}
	//: measured from the current version's own stamp.
	return current.Created.Add(r.policy.Every), nil
}

// RotateIfDue rotates when the current version is due and reports whether it
// did. A secret with no version is created instead — the same as Ensure — and
// reported as not rotated. The decision and the rotation are one step under
// the rotator's lock, and under the Locker's when one was configured.
func (r *Rotator) RotateIfDue(ctx context.Context) (current coresecret.VersionValue, rotated bool, err error) {
	serialErr := r.serialised(ctx, func() error {
		found, findErr := r.currentOrFirst(ctx)
		//: the store's verdict.
		if findErr != nil {
			//: nothing decided.
			return findErr
		}
		current = found
		//: not due yet — including a version Ensure just created.
		if r.clk.Now().Before(found.Created.Add(r.policy.Every)) {
			//: nothing to do.
			return nil
		}
		created, rotateErr := r.rotateLocked(ctx)
		//: a version was stored, even if the prune after it failed.
		if !created.Value.IsZero() {
			current, rotated = created, true
		}
		//: the rotation's verdict.
		return rotateErr
	})
	//: tell the caller's callback after every lock is released.
	if rotated {
		r.notify(current)
	}
	//: the current version, and whether this call made it.
	return current, rotated, serialErr
}

// Rotate rotates now, whether or not a rotation is due, and returns the new
// version. When the version was stored but the prune after it failed, it
// returns BOTH the new version and the prune's error: the rotation happened —
// OnRotate is told — and only the retirement of old versions did not, which
// the next rotation retries.
func (r *Rotator) Rotate(ctx context.Context) (created coresecret.VersionValue, err error) {
	serialErr := r.serialised(ctx, func() error {
		stored, rotateErr := r.rotateLocked(ctx)
		created = stored
		//: the rotation's verdict.
		return rotateErr
	})
	//: a version was stored, even if the prune after it failed.
	if !created.Value.IsZero() {
		r.notify(created)
	}
	//: the new version and the first failure, if any.
	return created, serialErr
}

// Run rotates the secret for as long as ctx lives: it ensures the secret
// exists, sleeps on the rotator's clock until the current version is due,
// rotates, and repeats. It blocks on the caller's goroutine and starts none,
// so the caller that runs it joins it by waiting for it to return.
//
// It returns nil when ctx ends — a stop is not a failure — and the first error
// otherwise, without retrying: a store that went away is the supervisor's to
// retry with a backoff, not a loop's to spin on.
func (r *Rotator) Run(ctx context.Context) error {
	//: one decision per due instant, until the context ends.
	for {
		current, _, stepErr := r.RotateIfDue(ctx)
		//: a failure — unless it is only the context ending.
		if stepErr != nil {
			//: a stop is not a failure.
			if ctx.Err() != nil {
				//: stopped.
				return nil
			}
			//: the supervisor's to handle.
			return stepErr
		}
		//: sleep until the current version is due, or the context ends.
		if !r.sleepUntil(ctx, current.Created.Add(r.policy.Every)) {
			//: stopped.
			return nil
		}
	}
}

// sleepUntil waits on the rotator's clock until due, reporting false when ctx
// ended first. A due instant already past — the store's clock and the
// rotator's disagree — waits one whole interval rather than spinning.
func (r *Rotator) sleepUntil(ctx context.Context, due time.Time) bool {
	wait := due.Sub(r.clk.Now())
	//: never a zero or negative timer: that would be a hot loop.
	if wait <= 0 {
		wait = r.policy.Every
	}
	timer := r.clk.NewTimer(wait)
	defer timer.Stop()
	//: whichever comes first.
	select {
	//: the context ended.
	case <-ctx.Done():
		//: stop.
		return false
	//: due.
	case <-timer.C():
		//: decide again.
		return true
	}
}

// currentOrFirst returns the current version, creating version 1 when there is
// none. The caller holds the rotator's lock.
func (r *Rotator) currentOrFirst(ctx context.Context) (current coresecret.VersionValue, err error) {
	current, getErr := r.store.Get(ctx, r.name)
	//: found, or a failure other than "there is none".
	if getErr == nil || !errs.HasCode(getErr, coresecret.CodeNotFound) {
		//: the version, or the store's verdict.
		return current, getErr
	}
	value, genErr := r.generate()
	//: nothing to store.
	if genErr != nil {
		//: GenerateFailed or the generator's own InvalidConfig.
		return coresecret.VersionValue{}, genErr
	}
	//: version 1.
	return r.store.Put(ctx, r.name, value)
}

// rotateLocked generates, stores and prunes. The caller holds the rotator's
// lock. A version returned with an error was stored, and only the prune
// failed.
func (r *Rotator) rotateLocked(ctx context.Context) (created coresecret.VersionValue, err error) {
	value, genErr := r.generate()
	//: nothing stored, nothing pruned: the current version stays current.
	if genErr != nil {
		//: GenerateFailed.
		return coresecret.VersionValue{}, genErr
	}
	created, putErr := r.store.Put(ctx, r.name, value)
	//: nothing stored.
	if putErr != nil {
		//: the store's verdict — ReadOnly for a store that only reads.
		return coresecret.VersionValue{}, putErr
	}
	//: the rotation happened; retire what the policy no longer keeps.
	return created, r.store.Prune(ctx, r.name, r.policy.Keep)
}

// generate runs the policy's generator and refuses an empty result.
func (r *Rotator) generate() (value coresecret.Value, err error) {
	value, genErr := r.policy.Generate()
	//: the generator's own failure, kept as the cause.
	if genErr != nil {
		//: an SDK verdict from the generator (Random's) is kept as it is.
		if typed, isSDK := errors.AsType[*errs.Error](genErr); isSDK && typed != nil {
			//: InvalidConfig or GenerateFailed, unchanged.
			return coresecret.Value{}, genErr
		}
		//: GenerateFailed, with the caller's message as a field.
		return coresecret.Value{}, wrapAs(GenerateFailed, genErr, errs.String("secret", r.name))
	}
	//: an empty secret would be refused by Put; say why here instead.
	if value.IsZero() {
		//: GenerateFailed.
		return coresecret.Value{}, wrapAs(GenerateFailed, nil, errs.String("secret", r.name), errs.String("problem", "empty"))
	}
	//: a new secret.
	return value, nil
}

// serialised runs work under the rotator's lock, and under the Locker's when
// one was configured, releasing both whatever work returns.
func (r *Rotator) serialised(ctx context.Context, work func() error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	//: one process: the mutex is the whole serialisation.
	if r.locker == nil {
		//: the work's verdict.
		return work()
	}
	lease, acquireErr := r.locker.Acquire(ctx, rotateLockPrefix+r.name)
	//: the caller gave up waiting, or the lock could not be taken.
	if acquireErr != nil {
		//: the caller's own deadline is reported as the caller's.
		if ctxErr := ctx.Err(); ctxErr != nil {
			//: context.Canceled or context.DeadlineExceeded.
			return ctxErr
		}
		//: StoreUnavailable: the coordination the caller asked for is down.
		return wrapAs(coresecret.StoreUnavailable, acquireErr, errs.String("secret", r.name), errs.String("operation", "lock"))
	}
	workErr := work()
	releaseErr := lease.Release(context.WithoutCancel(ctx))
	//: a release failure is reported beside the work's verdict, never instead.
	if releaseErr != nil {
		//: both, the work's first.
		return errors.Join(workErr, wrapAs(coresecret.StoreUnavailable, releaseErr,
			errs.String("secret", r.name), errs.String("operation", "unlock")))
	}
	//: the work's verdict.
	return workErr
}

// notify tells the caller's callback of a rotation, if there is one.
func (r *Rotator) notify(rotated coresecret.VersionValue) {
	//: nobody asked to be told.
	if r.onRotate == nil {
		//: nothing to call.
		return
	}
	r.onRotate(rotated)
}
