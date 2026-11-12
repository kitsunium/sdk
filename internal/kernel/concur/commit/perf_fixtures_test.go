//go:build !race

// The fixture of commit's performance contract (design/sdk.yaml, budgets): it
// sets one call up and returns it, and the gen_perf_test.go kit gen writes
// beside this file counts its allocations against its budget. Race off.
package commit_test

import (
	"context"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/concur/commit"
)

// perfDoIdleErr keeps each call's result alive.
var perfDoIdleErr error

// perfDoIdle is the idle Do's fixture (#813): a group over items of size
// zero, as vfs's directory flush builds, called with a context that can
// never end and nothing else calling — every call takes the idle path.
func perfDoIdle(tb testing.TB) func() {
	tb.Helper()
	group := commit.New(func(context.Context, []struct{}) error { return nil }, commit.Config{})
	tb.Cleanup(group.Close)
	ctx := context.Background()
	return func() {
		perfDoIdleErr = group.Do(ctx, struct{}{})
	}
}
