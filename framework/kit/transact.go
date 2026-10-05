package kit

import (
	"context"

	ikit "github.com/kitsunium/sdk/framework/internal/kit"
)

// Transact runs fn in one transaction and returns fn's error: nil commits
// what fn wrote, an error undoes it — a panic too, which then continues.
//
//	err := kit.Transact(ctx, func(ctx context.Context) error {
//		if _, err := Cases.Update(ctx, id, closeCase); err != nil {
//			return err // what this function wrote is undone
//		}
//		if err := Decisions.Insert(ctx, d); err != nil {
//			return err
//		}
//		return Changes.Publish(ctx, Closed{ID: id}) // held: queued after the commit
//	})
//
// A transaction belongs to one database: the one its first store call
// reaches — at its first call on a database, at its first write on the data
// directory, which counts as one, memory with it. Inside it, a store of
// another database is read as usual; writing one is refused ([Invalid],
// [CodeTransactionSpan]). A Transact inside another is a savepoint: its
// failure undoes its own writes, and the outer transaction goes on if its
// caller handles the error.
//
// What leaves the process waits for the outermost commit: a topic's
// publish, a queued command's dispatch, a mail — whose outbox ID [Mailer].Send
// returns at once —, a watch's notice, the store's write hooks that wake
// workflows and loops, a workflow's OnTransition hooks. Each is checked when
// it is made, so a caller's mistake returns to the caller; a rollback drops
// them, and a savepoint's rollback its own. One a queue refuses after the
// commit is logged and reported, and the commit stands; a process that dies
// between the commit and the release loses what was held.
//
// On a database the transaction is the database's own, at its default
// isolation. On the data directory and in memory kit keeps its own: it takes
// the data's writer turn — one transaction at a time; a write outside any
// waits for it, and such writes share the turn among themselves —, its
// writes land at once, and an error writes each entity's previous value
// back, newest first. It is atomic against an error, not against a crash,
// and a reader may see a write before its commit. A store kept in memory by
// [InMemory] — a cache — is in no transaction: its writes land and stay.
//
// A command's handler runs in one already ([NoTransaction] opts out), and
// so does a workflow's transition.
func Transact(ctx context.Context, fn func(context.Context) error) error {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.Transact(ctx, fn)
}
