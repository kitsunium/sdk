package queueengine

import (
	"context"
	"time"

	"github.com/kitsunium/sdk/framework/internal/app/plug"
	"github.com/kitsunium/sdk/pkg/v1/data/queue"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// A mailer's outbox on a database (ADR 0178 C1, C7 item 7): what the spool
// writes is the whole mail as JSON, which kit seals in a broker that stands
// in front of the SQL one (sealedOutbox) — kit's core says how, through the
// queue's configuration (plug.QueueConfig's Seal and Unseal), since the
// sealing is its own. A mail that does not open is handed on as it lies:
// the spool does not decode it, and dead-letters it as such.

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
	release queue.Releaser
	manager queue.DeadLetterManager
	seal    func(ctx context.Context, payload []byte) ([]byte, error)
	unseal  func(ctx context.Context, box []byte) []byte
	// node is the mailer's node ID, max the bound of a mail before it is
	// sealed.
	node string
	max  int
}

// newSealedOutbox is inner, a SQL broker, behind the sealing cfg says. A
// broker without one of the SQL broker's siblings is refused: the spool
// would lose what it relies on.
func newSealedOutbox(inner queue.Broker, cfg *plug.QueueConfig) (*sealedOutbox, error) {
	o := &sealedOutbox{Broker: inner, seal: cfg.Seal, unseal: cfg.Unseal, node: cfg.Name, max: cfg.MaxPayload}
	var ok [6]bool
	o.waker, ok[0] = inner.(queue.Waker)
	o.renew, ok[1] = inner.(queue.LeaseExtender)
	o.dead, ok[2] = inner.(queue.DeadLetterReader)
	o.reject, ok[3] = inner.(queue.Rejecter)
	o.manager, ok[4] = inner.(queue.DeadLetterManager)
	o.release, ok[5] = inner.(queue.Releaser)
	for _, has := range ok {
		if !has {
			return nil, errs.Wrap(queue.QueueMisconfigured, errs.WrapParams{},
				errs.String("mailer", o.node), errs.String("problem", "a mailer's outbox broker lacks a capability the spool needs"))
		}
	}
	return o, nil
}

// Publish seals payload — a mail as the spool writes it — and publishes the
// box, in the transaction ctx carries when there is one. A mail larger than
// the bound is the queue's MessageTooLarge, as the spool's own queue would
// say it, and nothing is published.
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

// open is the mail a box holds, the box as it lies when it does not open.
func (o *sealedOutbox) open(ctx context.Context, payload []byte) []byte {
	if o.unseal == nil {
		return payload
	}
	return o.unseal(ctx, payload)
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

// Release is the SQL broker's: a mail the spool did not finish goes back
// as it lies, its attempt not counted.
func (o *sealedOutbox) Release(ctx context.Context, receipt queue.Receipt) error {
	return o.release.Release(ctx, receipt)
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

// sealMail seals a mail as Publish does, refusing one above the bound.
func (o *sealedOutbox) sealMail(ctx context.Context, payload []byte) ([]byte, error) {
	if o.max > 0 && len(payload) > o.max {
		return nil, errs.Wrap(queue.MessageTooLarge, errs.WrapParams{},
			errs.String("mailer", o.node), errs.Int("bytes", len(payload)), errs.Int("max", o.max))
	}
	return o.seal(ctx, payload)
}

// lacks is the refusal of a capability the broker behind the sealing does
// not have.
func (o *sealedOutbox) lacks(capability string) error {
	return errs.Wrap(queue.QueueMisconfigured, errs.WrapParams{},
		errs.String("mailer", o.node), errs.String("capability", capability))
}

// NackAfter is the SQL broker's (queue.DelayedNacker): the spool parks a
// mail for its backoff with it, the box staying as it lies.
func (o *sealedOutbox) NackAfter(ctx context.Context, receipt queue.Receipt, cause error, delay time.Duration) (queue.Nack, error) {
	nacker, ok := o.Broker.(queue.DelayedNacker)
	if !ok {
		return queue.Nack{}, o.lacks("DelayedNacker")
	}
	return nacker.NackAfter(ctx, receipt, cause, delay)
}

// PublishAt seals payload as Publish does and publishes the box, received by
// no consumer before at (queue.DelayedPublisher).
func (o *sealedOutbox) PublishAt(ctx context.Context, payload []byte, at time.Time) (queue.Message, error) {
	deferred, ok := o.Broker.(queue.DelayedPublisher)
	if !ok {
		return queue.Message{}, o.lacks("DelayedPublisher")
	}
	box, err := o.sealMail(ctx, payload)
	if err != nil {
		return queue.Message{}, err
	}
	return deferred.PublishAt(ctx, box, at)
}

// PublishOnce seals payload as Publish does and publishes the box at most
// once per key over window (queue.OncePublisher).
func (o *sealedOutbox) PublishOnce(ctx context.Context, key string, payload []byte, window time.Duration) (queue.Message, bool, error) {
	once, ok := o.Broker.(queue.OncePublisher)
	if !ok {
		return queue.Message{}, false, o.lacks("OncePublisher")
	}
	box, err := o.sealMail(ctx, payload)
	if err != nil {
		return queue.Message{}, false, err
	}
	return once.PublishOnce(ctx, key, box, window)
}

// ReplayDeadLetter is the SQL broker's: the box goes back as it lies.
func (o *sealedOutbox) ReplayDeadLetter(ctx context.Context, id string) error {
	return o.manager.ReplayDeadLetter(ctx, id)
}

// DeleteDeadLetter is the SQL broker's.
func (o *sealedOutbox) DeleteDeadLetter(ctx context.Context, id string) error {
	return o.manager.DeleteDeadLetter(ctx, id)
}
