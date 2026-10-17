package kit

import (
	"bytes"
	"context"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/data/queue"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// A mailer's outbox on a database (ADR 0178 C1, C7 item 7). A mailer whose
// service a database keeps — by a store's rules, the database that would
// keep its queue (queueKeeper) — keeps its outbox in one table of the SDK's
// SQL broker there, kit_queue__<service>__<mailer>__outbox, instead of the
// spool's file queue under <data>/<service>/outbox/<mailer>. A mail sent in
// a transaction on that database is written in it, just before its commit
// (txn.join): it exists if and only if the transaction commits, and the
// spool, handed the database's transaction manager (mailspool.Config.
// Deferrer), tells the mailbox it was queued after the commit and nothing
// when it rolls back. A mail sent elsewhere — outside a transaction, in one
// on another database or on the data directory — is put in the outbox at
// once or after the commit, as before.
//
// What the spool writes is the whole mail as JSON — addresses, subject,
// bodies, attachments —: kit seals it in a broker that stands in front of
// the SQL one (sealedOutbox), under data-key itself, bound to the mailer.
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
// around it. The bound on the mail itself is maxMailBytes, which
// sealedOutbox applies before it seals.
const sealedMailBytes int = maxMailBytes/3*4 + 64<<10

// sealedOutbox is the broker a mailer's spool writes into and delivers from
// when its outbox is on a database: the SQL broker, each payload sealed on
// its way in and opened on its way out — a delivery's and a dead letter's.
// It answers every sibling the SQL broker answers, so the spool keeps its
// wake, its lease renewal and its dead letters.
type sealedOutbox struct {
	queue.Broker
	waker   queue.Waker
	renew   queue.LeaseExtender
	dead    queue.DeadLetterReader
	reject  queue.Rejecter
	manager queue.DeadLetterManager
	z       *sealer
	// node is the mailer's node ID, which every box is bound to.
	node string
}

// newSealedOutbox is inner, a SQL broker, behind the sealing of the
// mailer node's outbox under z. A broker without one of the SQL broker's
// siblings is refused: the spool would lose what it relies on.
func newSealedOutbox(inner queue.Broker, z *sealer, node string) (*sealedOutbox, error) {
	o := &sealedOutbox{Broker: inner, z: z, node: node}
	var ok [5]bool
	o.waker, ok[0] = inner.(queue.Waker)
	o.renew, ok[1] = inner.(queue.LeaseExtender)
	o.dead, ok[2] = inner.(queue.DeadLetterReader)
	o.reject, ok[3] = inner.(queue.Rejecter)
	o.manager, ok[4] = inner.(queue.DeadLetterManager)
	for _, has := range ok {
		if !has {
			return nil, failure(CodeMailQueue, "OUTBOX_BROKER", "a mailer's outbox broker lacks a capability the spool needs", nil,
				errs.String("mailer", node))
		}
	}
	return o, nil
}

// Publish seals payload — a mail as the spool writes it — and publishes the
// box, in the transaction ctx carries when there is one. A mail larger than
// maxMailBytes is the queue's MessageTooLarge, as the spool's own queue
// would say it, and nothing is published.
func (o *sealedOutbox) Publish(ctx context.Context, payload []byte) (queue.Message, error) {
	box, err := o.sealMail(ctx, payload)
	if err != nil {
		return queue.Message{}, err
	}
	return o.Broker.Publish(ctx, box)
}

// Receive leases up to maxMessages mails and opens each.
func (o *sealedOutbox) Receive(ctx context.Context, maxMessages int) ([]queue.Delivery, error) {
	deliveries, err := o.Broker.Receive(ctx, maxMessages)
	for i := range deliveries {
		deliveries[i].Message.Payload = o.open(ctx, deliveries[i].Message.Payload)
	}
	return deliveries, err
}

// open is the mail a box holds. A box that does not open — data-key's
// version that sealed it pruned, or the box altered — is handed on as it
// lies: the spool does not decode it, and dead-letters it as such, its
// queue identifier and its cause kept as evidence. A payload that is no box
// is handed on unchanged.
func (o *sealedOutbox) open(ctx context.Context, payload []byte) []byte {
	if !bytes.HasPrefix(payload, []byte(`"`+boxPrefix)) {
		return payload
	}
	plain, err := o.z.open(ctx, payload, o.node, outboxBind)
	if err != nil {
		return payload
	}
	return plain
}

// Wake is the SQL broker's.
func (o *sealedOutbox) Wake() queue.Wake { return o.waker.Wake() }

// Extend is the SQL broker's.
func (o *sealedOutbox) Extend(ctx context.Context, receipt queue.Receipt, by time.Duration) (queue.Lease, error) {
	return o.renew.Extend(ctx, receipt, by)
}

// Reject is the SQL broker's.
func (o *sealedOutbox) Reject(ctx context.Context, receipt queue.Receipt, cause error) error {
	return o.reject.Reject(ctx, receipt, cause)
}

// DeadLetters returns up to maxLetters dead mails, oldest first, each
// opened as a delivery is.
func (o *sealedOutbox) DeadLetters(ctx context.Context, maxLetters int) ([]queue.DeadLetter, error) {
	dead, err := o.dead.DeadLetters(ctx, maxLetters)
	for i := range dead {
		dead[i].Message.Payload = o.open(ctx, dead[i].Message.Payload)
	}
	return dead, err
}

// Messages lists up to maxMessages live mails after an identifier, each
// opened as a delivery is (queue.MessageReader): what a data copy
// fingerprints, the mail and not its box.
func (o *sealedOutbox) Messages(ctx context.Context, after string, maxMessages int) ([]queue.LiveMessage, error) {
	reader, ok := o.Broker.(queue.MessageReader)
	if !ok {
		return nil, o.lacks("MessageReader")
	}
	live, err := reader.Messages(ctx, after, maxMessages)
	for i := range live {
		live[i].Message.Payload = o.open(ctx, live[i].Message.Payload)
	}
	return live, err
}

// PutMessage seals a mail, as it waits in a file outbox, and writes the box
// into the table with its count and its instants (queue.MessageWriter):
// what a data copy moves a mailer's outbox with.
func (o *sealedOutbox) PutMessage(ctx context.Context, live queue.LiveMessage) (string, error) {
	writer, ok := o.Broker.(queue.MessageWriter)
	if !ok {
		return "", o.lacks("MessageWriter")
	}
	box, err := o.sealMail(ctx, live.Message.Payload)
	if err != nil {
		return "", err
	}
	live.Message.Payload = box
	return writer.PutMessage(ctx, live)
}

// PutDeadLetter seals a dead mail and writes the box into the table's dead
// letters with its cause (queue.DeadLetterWriter).
func (o *sealedOutbox) PutDeadLetter(ctx context.Context, dead queue.DeadLetter) (string, error) {
	writer, ok := o.Broker.(queue.DeadLetterWriter)
	if !ok {
		return "", o.lacks("DeadLetterWriter")
	}
	box, err := o.sealMail(ctx, dead.Message.Payload)
	if err != nil {
		return "", err
	}
	dead.Message.Payload = box
	return writer.PutDeadLetter(ctx, dead)
}

// sealMail seals a mail as Publish does, refusing one above maxMailBytes.
func (o *sealedOutbox) sealMail(ctx context.Context, payload []byte) ([]byte, error) {
	if len(payload) > maxMailBytes {
		return nil, errs.Wrap(queue.MessageTooLarge, errs.WrapParams{},
			errs.String("mailer", o.node), errs.Int("bytes", len(payload)), errs.Int("max", maxMailBytes))
	}
	return o.z.seal(ctx, "", payload, o.node, outboxBind)
}

// lacks is the refusal of a capability the broker behind the sealing does
// not have.
func (o *sealedOutbox) lacks(capability string) error {
	return failure(CodeMailQueue, "OUTBOX_BROKER", "a mailer's outbox broker lacks a capability the copy needs", nil,
		errs.String("mailer", o.node), errs.String("capability", capability))
}

// ReplayDeadLetter is the SQL broker's: the box goes back as it lies.
func (o *sealedOutbox) ReplayDeadLetter(ctx context.Context, id string) error {
	return o.manager.ReplayDeadLetter(ctx, id)
}

// DeleteDeadLetter is the SQL broker's.
func (o *sealedOutbox) DeleteDeadLetter(ctx context.Context, id string) error {
	return o.manager.DeleteDeadLetter(ctx, id)
}

// outboxPolicy is the policy of a mailer's outbox on a database: the one the
// spool gives the queue it builds — a lease of twice an attempt's bound, a
// retry from the mailer's first backoff, its attempts —, its bound a sealed
// mail's.
func (m *Mailer) outboxPolicy() queue.Policy {
	return queue.Policy{
		VisibilityTimeout: 2 * mailSendTimeout, RetryDelay: mailBackoffFirst,
		MaxDeliveries: m.opts.maxAttempts, MaxMessageBytes: sealedMailBytes,
	}
}

// openOutbox opens the mailer's outbox on the database that keeps its
// service, when that database is open: the SQL broker behind the sealing,
// behind a test's wrapper. The spool takes the database's transaction
// manager too, to announce a mail after the commit and forget it after a
// rollback (deferrerOf). It returns no database where the outbox stays the
// spool's own.
func (m *Mailer) openOutbox(ctx context.Context, a *App) (db *databaseRun, broker queue.Broker, err error) {
	r := a.queueDatabase(&m.nodeBase)
	if r == nil {
		return nil, nil, nil
	}
	z, err := a.sealing(ctx)
	if err != nil {
		return nil, nil, err
	}
	inner, err := a.openSQLQueue(&m.nodeBase, underOutbox, r, m.outboxPolicy())
	if err != nil {
		return nil, nil, err
	}
	sealed, err := newSealedOutbox(inner, z, m.id)
	if err != nil {
		return nil, nil, err
	}
	return r, a.wrapped(sealed), nil
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
