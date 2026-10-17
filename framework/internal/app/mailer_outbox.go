package app

import (
	"bytes"
	"context"

	"github.com/kitsunium/sdk/framework/internal/app/plug"
)

// A mailer's outbox on a database (ADR 0178 C1, C7 item 7). A mailer whose
// service a database keeps — by a store's rules, the database that would
// keep its queue (queueKeeper) — keeps its outbox in one table of the SDK's
// SQL broker there, kit_queue__<service>__<mailer>__outbox, instead of the
// spool's file queue under <data>/<service>/outbox/<mailer>. A mail sent in
// a transaction on that database is written in it, just before its commit
// (txn.join): it exists if and only if the transaction commits, and the
// spool, handed the database's transaction manager by the mail subsystem
// (framework/internal/app/mailengine, ADR 0179 step 6), tells the mailbox it
// was queued after the commit and nothing when it rolls back. A mail sent elsewhere — outside a transaction, in one
// on another database or on the data directory — is put in the outbox at
// once or after the commit, as before.
//
// What the spool writes is the whole mail as JSON — addresses, subject,
// bodies, attachments —: kit seals it in a broker that stands in front of
// the SQL one, under data-key itself, bound to the mailer. The broker is the
// queue subsystem's (framework/internal/app/queueengine, ADR 0179 step 4); the
// sealing is kit's, handed to it in the queue's configuration (sealOutbox).
// A mail names no subject kit can read, so no person's key seals it, and a
// person's erasure does not reach it; a mail waiting, or dead, longer than
// data-key's three kept versions stops opening, and is dead-lettered as a
// mail that does not decode, which suits mail. Without a database the
// outbox stays the spool's own, unsealed, as it was.

// outboxBind is what a sealed mail is bound to, beside its mailer's node.
const outboxBind = "outbox"

// sealedMailBytes bounds one sealed mail in the outbox's table: a mail of
// maxMailBytes as the spool writes it, padded, sealed and in base64 — four
// bytes for three — with room for the box's header and the JSON string
// around it. The bound on the mail itself is maxMailBytes, which the sealed
// outbox applies before it seals.
const sealedMailBytes int = maxMailBytes/3*4 + 64<<10

// sealOutbox has the queue cfg places seal every mail on its way in and open
// it on its way out, under data-key, bound to the mailer m: a mail above
// maxMailBytes is the queue's MessageTooLarge, and a box that does not open —
// data-key's version that sealed it pruned, or the box altered — is handed
// on as it lies, so that the spool dead-letters it as a mail that does not
// decode, its queue identifier and its cause kept as evidence. A payload
// that is no box is handed on unchanged.
func (m *Mailer) sealOutbox(ctx context.Context, a *App, cfg *plug.QueueConfig) error {
	z, err := a.sealing(ctx)
	if err != nil {
		return err
	}
	node := m.id
	cfg.MaxPayload = maxMailBytes
	cfg.Seal = func(ctx context.Context, payload []byte) ([]byte, error) {
		return z.seal(ctx, "", payload, node, outboxBind)
	}
	cfg.Unseal = func(ctx context.Context, box []byte) []byte {
		if !bytes.HasPrefix(box, []byte(`"`+boxPrefix)) {
			return box
		}
		plain, err := z.open(ctx, box, node, outboxBind)
		if err != nil {
			return box
		}
		return plain
	}
	return nil
}

// outboxPolicy is the policy of a mailer's outbox on a database: the one the
// spool gives the queue it builds — a lease of twice an attempt's bound, a
// retry from the mailer's first backoff, its attempts —, its bound a sealed
// mail's.
func (m *Mailer) outboxPolicy() plug.QueuePolicyValue {
	return plug.QueuePolicyValue{
		VisibilityTimeout: 2 * mailSendTimeout, RetryDelay: mailBackoffFirst,
		MaxDeliveries: m.opts.maxAttempts, MaxMessageBytes: sealedMailBytes,
	}
}

// openOutbox opens the mailer's outbox on the database that keeps its
// service, when that database is open: the SQL broker behind the sealing,
// behind a test's wrapper. The spool takes the database's transaction
// manager too, to announce a mail after the commit and forget it after a
// rollback (mailengine's deferrerOf). It returns no database where the outbox
// stays the spool's own.
func (m *Mailer) openOutbox(ctx context.Context, a *App) (db *databaseRun, broker plug.Broker, err error) {
	r := a.queueDatabase(&m.nodeBase)
	if r == nil {
		return nil, nil, nil
	}
	cfg := plug.QueueConfig{Policy: m.outboxPolicy()}
	if err := m.sealOutbox(ctx, a, &cfg); err != nil {
		return nil, nil, err
	}
	broker, err = a.openSQLQueue(&m.nodeBase, underOutbox, r, cfg)
	if err != nil {
		return nil, nil, err
	}
	return r, broker, nil
}

// queueUnder is what the mailer's outbox is kept under, beside its name:
// <data>/<service>/outbox/<mailer> on disk, the table
// kit_queue__<service>__<mailer>__outbox on a database.
func (m *Mailer) queueUnder() string { return underOutbox }

// sealsAtRest reports whether the mailer's mails are sealed where they wait:
// its outbox is on a database (ADR 0178 C7 item 7). An outbox on disk
// without one is the spool's own, unsealed.
func (m *Mailer) sealsAtRest(a *App) bool {
	return a.sealsOnDisk() && queueWhere(a, &m.nodeBase) == "sql"
}
