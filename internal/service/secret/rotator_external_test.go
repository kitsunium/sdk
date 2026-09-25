package secret_test

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	coresecret "github.com/kitsunium/sdk/internal/core/secret"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svclock "github.com/kitsunium/sdk/internal/service/lock"
	svcsecret "github.com/kitsunium/sdk/internal/service/secret"
)

// rotationEvery is the interval every rotator in this file runs on.
const rotationEvery time.Duration = 24 * time.Hour

// rotatorFixture is a manual clock, a memory store stamping on it, a rotator
// over "signing-key" keeping three versions, and the record of every rotation
// OnRotate was told of.
type rotatorFixture struct {
	clk     *clock.ManualClock
	store   coresecret.Store
	rotator *svcsecret.Rotator
	mu      sync.Mutex
	told    []int
	// events receives each rotated version as OnRotate is told of it.
	events chan int
}

// newRotatorFixture builds the fixture.
func newRotatorFixture(t *testing.T) *rotatorFixture {
	t.Helper()
	fixture := &rotatorFixture{clk: clock.NewManualClock(epoch), events: make(chan int, 16)}
	fixture.store = svcsecret.NewMemory(svcsecret.MemoryConfig{Clock: fixture.clk})
	rotator, err := svcsecret.NewRotator(svcsecret.RotatorConfig{
		Store:  fixture.store,
		Name:   "signing-key",
		Policy: svcsecret.PolicySpec{Every: rotationEvery, Keep: 3, Generate: svcsecret.Random(32)},
		Clock:  fixture.clk,
		OnRotate: func(rotated coresecret.VersionValue) {
			fixture.mu.Lock()
			defer fixture.mu.Unlock()
			fixture.told = append(fixture.told, rotated.Version)
			fixture.events <- rotated.Version
		},
	})
	if err != nil {
		t.Fatalf("NewRotator: %v", err)
	}
	fixture.rotator = rotator
	return fixture
}

// rotations returns the versions OnRotate was told of.
func (f *rotatorFixture) rotations() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.told)
}

// TestRotatorOnAManualClock walks one secret through its life on a clock the
// test moves: created, not due, due, rotated, pruned to the policy's Keep.
func TestRotatorOnAManualClock(t *testing.T) {
	t.Parallel()
	f := newRotatorFixture(t)
	ctx := t.Context()
	if _, err := f.rotator.Due(ctx); !errs.HasCode(err, coresecret.CodeNotFound) {
		t.Fatalf("Due before Ensure = %v, want NotFound", err)
	}
	first, err := f.rotator.Ensure(ctx)
	if err != nil || first.Version != 1 || first.Value.Len() != 32 {
		t.Fatalf("Ensure = (v%d, %d bytes, %v), want v1 of 32 bytes", first.Version, first.Value.Len(), err)
	}
	again, err := f.rotator.Ensure(ctx)
	if err != nil || again.Version != 1 {
		t.Fatalf("a second Ensure = (v%d, %v), want the same v1", again.Version, err)
	}
	due, err := f.rotator.Due(ctx)
	if err != nil || !due.Equal(epoch.Add(rotationEvery)) {
		t.Fatalf("Due = (%v, %v), want %v", due, err, epoch.Add(rotationEvery))
	}
	//: one nanosecond early: not due.
	f.clk.Advance(rotationEvery - time.Nanosecond)
	current, rotated, err := f.rotator.RotateIfDue(ctx)
	if err != nil || rotated || current.Version != 1 {
		t.Fatalf("RotateIfDue early = (v%d, %v, %v), want v1 not rotated", current.Version, rotated, err)
	}
	//: due, at the exact instant.
	f.clk.Advance(time.Nanosecond)
	current, rotated, err = f.rotator.RotateIfDue(ctx)
	if err != nil || !rotated || current.Version != 2 || !current.Created.Equal(f.clk.Now()) {
		t.Fatalf("RotateIfDue when due = (v%d, %v, %v)", current.Version, rotated, err)
	}
	//: the next due instant is measured from the NEW version.
	if next, dueErr := f.rotator.Due(ctx); dueErr != nil || !next.Equal(f.clk.Now().Add(rotationEvery)) {
		t.Fatalf("Due after rotation = (%v, %v)", next, dueErr)
	}
	//: rotate three more times; Keep 3 retires the oldest each time.
	for range 3 {
		if _, rotateErr := f.rotator.Rotate(ctx); rotateErr != nil {
			t.Fatalf("Rotate: %v", rotateErr)
		}
	}
	versions, err := f.store.Versions(ctx, "signing-key")
	if err != nil || !slices.Equal(numbers(versions), []int{5, 4, 3}) {
		t.Fatalf("kept = (%v, %v), want [5 4 3]", numbers(versions), err)
	}
	if got := f.rotations(); !slices.Equal(got, []int{2, 3, 4, 5}) {
		t.Fatalf("OnRotate was told of %v, want [2 3 4 5] — Ensure's v1 is not a rotation", got)
	}
}

// TestRotatorRunJoinsAndRotatesOnSchedule drives Run on the manual clock: it
// rotates each time the clock reaches the due instant, starts no goroutine of
// its own, and returns nil when its context ends.
func TestRotatorRunJoinsAndRotatesOnSchedule(t *testing.T) {
	t.Parallel()
	f := newRotatorFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- f.rotator.Run(ctx) }()
	for want := 2; want <= 4; want++ {
		//: Run has ensured the secret and armed its timer.
		f.clk.BlockUntil(1)
		f.clk.Advance(rotationEvery)
		//: the rotation this advance made due.
		if got := <-f.events; got != want {
			t.Fatalf("Run rotated to v%d, want v%d", got, want)
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run returned %v after its context ended, want nil", err)
	}
	if got := f.rotations(); !slices.Equal(got, []int{2, 3, 4}) {
		t.Fatalf("Run rotated %v, want [2 3 4]", got)
	}
}

// TestRotatorRefusesAnInertPolicy pins every construction refusal: none of
// the policy's fields has a default the SDK could choose.
func TestRotatorRefusesAnInertPolicy(t *testing.T) {
	t.Parallel()
	store := svcsecret.NewMemory(svcsecret.MemoryConfig{})
	valid := svcsecret.PolicySpec{Every: time.Hour, Keep: 2, Generate: svcsecret.Random(32)}
	type tc struct {
		name   string
		mutate func(c *svcsecret.RotatorConfig)
		code   errs.Code
	}
	tests := []tc{
		{"no store", func(c *svcsecret.RotatorConfig) { c.Store = nil }, svcsecret.CodeInvalidConfig},
		{"a bad name", func(c *svcsecret.RotatorConfig) { c.Name = "Bad" }, coresecret.CodeInvalidName},
		{"no interval", func(c *svcsecret.RotatorConfig) { c.Policy.Every = 0 }, svcsecret.CodeInvalidConfig},
		{"keeping one", func(c *svcsecret.RotatorConfig) { c.Policy.Keep = 1 }, svcsecret.CodeInvalidConfig},
		{"no generator", func(c *svcsecret.RotatorConfig) { c.Policy.Generate = nil }, svcsecret.CodeInvalidConfig},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		cfg := svcsecret.RotatorConfig{Store: store, Name: "key", Policy: valid}
		c.mutate(&cfg)
		if _, err := svcsecret.NewRotator(cfg); !errs.HasCode(err, c.code) {
			t.Errorf("%s: NewRotator = %v, want %v", c.name, err, c.code)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestRotatorGeneratorFailuresChangeNothing pins that a rotation whose
// generator fails stores nothing and prunes nothing.
func TestRotatorGeneratorFailuresChangeNothing(t *testing.T) {
	t.Parallel()
	store := svcsecret.NewMemory(svcsecret.MemoryConfig{})
	if _, err := store.Put(t.Context(), "key", coresecret.FromString("original")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	type tc struct {
		name     string
		generate func() (coresecret.Value, error)
		code     errs.Code
	}
	tests := []tc{
		{"a secret too short to be one", svcsecret.Random(8), svcsecret.CodeInvalidConfig},
		{"an empty secret", func() (coresecret.Value, error) { return coresecret.Value{}, nil }, svcsecret.CodeGenerateFailed},
		{"a generator error", func() (coresecret.Value, error) {
			return coresecret.Value{}, context.DeadlineExceeded
		}, svcsecret.CodeGenerateFailed},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		rotator, err := svcsecret.NewRotator(svcsecret.RotatorConfig{
			Store: store, Name: "key", Policy: svcsecret.PolicySpec{Every: time.Hour, Keep: 2, Generate: c.generate},
		})
		if err != nil {
			t.Fatalf("NewRotator: %v", err)
		}
		if _, rotateErr := rotator.Rotate(t.Context()); !errs.HasCode(rotateErr, c.code) {
			t.Fatalf("%s: Rotate = %v, want %v", c.name, rotateErr, c.code)
		}
		current, getErr := store.Get(t.Context(), "key")
		if getErr != nil || current.Version != 1 || current.Value.RevealString() != "original" {
			t.Fatalf("%s: a failed rotation changed the store: (v%d, %v)", c.name, current.Version, getErr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			runCase(t, c)
		})
	}
}

// TestRotatorOverAReadOnlyStore pins that rotating what the process cannot
// write fails loudly, while Ensure still reads what the environment supplies.
func TestRotatorOverAReadOnlyStore(t *testing.T) {
	t.Setenv(envPrefix+"_MOUNTED_KEY", "mounted")
	rotator, err := svcsecret.NewRotator(svcsecret.RotatorConfig{
		Store: newEnvStore(t), Name: "mounted-key",
		Policy: svcsecret.PolicySpec{Every: time.Hour, Keep: 2, Generate: svcsecret.Random(32)},
	})
	if err != nil {
		t.Fatalf("NewRotator: %v", err)
	}
	current, err := rotator.Ensure(t.Context())
	if err != nil || current.Value.RevealString() != "mounted" {
		t.Fatalf("Ensure = (%q, %v)", current.Value.RevealString(), err)
	}
	if _, rotateErr := rotator.Rotate(t.Context()); !errs.HasCode(rotateErr, coresecret.CodeReadOnly) {
		t.Fatalf("Rotate over the environment = %v, want ReadOnly", rotateErr)
	}
}

// TestRotatorSerialisesThroughALocker pins the cross-process option: with a
// Locker, concurrent RotateIfDue calls through two rotators — two processes'
// worth — rotate a due secret exactly once.
func TestRotatorSerialisesThroughALocker(t *testing.T) {
	t.Parallel()
	clk := clock.NewManualClock(epoch)
	store := svcsecret.NewMemory(svcsecret.MemoryConfig{Clock: clk})
	locker, err := svclock.NewMemory(svclock.MemoryConfig{TTL: time.Hour, Clock: clk})
	if err != nil {
		t.Fatalf("lock.NewMemory: %v", err)
	}
	build := func() *svcsecret.Rotator {
		rotator, buildErr := svcsecret.NewRotator(svcsecret.RotatorConfig{
			Store: store, Name: "shared-key", Clock: clk, Locker: locker,
			Policy: svcsecret.PolicySpec{Every: time.Hour, Keep: 5, Generate: svcsecret.Random(32)},
		})
		if buildErr != nil {
			t.Fatalf("NewRotator: %v", buildErr)
		}
		return rotator
	}
	replicas := []*svcsecret.Rotator{build(), build(), build(), build()}
	if _, err := replicas[0].Ensure(t.Context()); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	clk.Advance(time.Hour)
	var wg sync.WaitGroup
	for _, replica := range replicas {
		wg.Go(func() {
			if _, _, rotateErr := replica.RotateIfDue(t.Context()); rotateErr != nil {
				t.Errorf("RotateIfDue: %v", rotateErr)
			}
		})
	}
	wg.Wait()
	versions, err := store.Versions(t.Context(), "shared-key")
	if err != nil || !slices.Equal(numbers(versions), []int{2, 1}) {
		t.Fatalf("versions = (%v, %v), want [2 1] — a due secret rotated more than once", numbers(versions), err)
	}
}
