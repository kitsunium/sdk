// Package kit — process profiles: what an app does with its process (ADR
// 0147 §5) — serve, run as a daemon, or run one CLI command — and the
// singleton lock.
package kit

import (
	"context"
	"time"

	ikit "github.com/kitsunium/sdk/framework/internal/kit"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// CodeSingletonHeld reports a start refused because another process of the
// app holds its singleton lock.
const CodeSingletonHeld errs.Code = ikit.CodeSingletonHeld

// Profile selects the app's process profile: model.ProfileServer (the
// default), model.ProfileDaemon or model.ProfileCLI.
func Profile(p string) AppOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.Profile(p)
}

// IdleStop makes a daemon stop itself once, for d, no connection was open on
// any of its listeners and no activity of its services (Service.Activity)
// said it was busy: a daemon launched on demand by its clients ends when
// they stop coming. It is refused outside the daemon profile.
func IdleStop(d time.Duration) AppOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.IdleStop(d)
}

// Singleton keeps one process of the app alive per scope on this machine: the
// start takes an exclusive file lock named after scope in the app's runtime
// directory (pkg/v1/app/lock: flock, LockFileEx) and holds it until the process
// ends. A second process refuses to start with CodeSingletonHeld — for a
// daemon's client, the sign to talk to the one that runs. The kernel drops
// the lock with a dead process.
func Singleton(scope string) AppOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.Singleton(scope)
}

// SingletonPer keeps one process of the app alive per value of scopes on
// this machine — per user, per installed executable, per configuration
// directory ([PerUID], [PerExecutable], [PerConfigDir], [PerEnv]) —: the lock
// is named after the scopes' key ([ScopeKey]), computed at the start, and
// held as Singleton's is. A client that must find the process computes the
// same key from the same scopes.
func SingletonPer(scopes ...Scope) AppOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.SingletonPer(scopes...)
}

// Stop asks the app ctx runs in to end its run: Run returns as it does on a
// signal, its stop draining every component — a daemon told "stop" by its
// client, on its listener, ends itself so. It returns at once, before the
// stop; it reports false when ctx runs in no app — a handler's, a loop's, a
// command's context does — or the app does not run.
func Stop(ctx context.Context) bool {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.Stop(ctx)
}
