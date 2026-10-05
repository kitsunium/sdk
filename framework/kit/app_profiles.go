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

// profile is Profile's body: decl_gen.go writes Profile, from the
// design, as one call of it.
func profile(p string) AppOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.Profile(p)
}

// idleStop is IdleStop's body: decl_gen.go writes IdleStop, from the
// design, as one call of it.
func idleStop(d time.Duration) AppOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.IdleStop(d)
}

// singleton is Singleton's body: decl_gen.go writes Singleton, from the
// design, as one call of it.
func singleton(scope string) AppOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.Singleton(scope)
}

// singletonPer is SingletonPer's body: decl_gen.go writes SingletonPer, from the
// design, as one call of it.
func singletonPer(scopes ...Scope) AppOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.SingletonPer(scopes...)
}

// stop is Stop's body: decl_gen.go writes Stop, from the
// design, as one call of it.
func stop(ctx context.Context) bool {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.Stop(ctx)
}
