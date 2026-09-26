// Package spool is outbound mail made durable: Send validates a mail and
// queues it, and the spool's consumer hands it to a mail transport, retrying a
// failure with a backoff and dead-lettering the mail after its last attempt —
// sending a delivered mail twice never, and a retried one always under the
// same Message-ID. ADR 0111.
//
// # Where it sits
//
// It composes two domains and adds what neither has. The mail domain composes
// and refuses a mail and hands it to a transport, synchronously, once. The
// queue domain keeps bytes durably and delivers them at least once. The spool
// is the mail-shaped use of the queue: its records are mails, a failed
// delivery waits a backoff that grows with the attempt instead of the queue's
// fixed retry delay, the last failure is the dead letter's reason, and a
// redelivered mail that was in fact delivered is recognised and dropped.
//
// # What Send promises
//
// Send refuses, at the call site, everything the transport would refuse
// later: a header carrying a line break, an address that is not one, a mail
// with no recipient or no body. It stamps what a retry must not change — the
// sender when the mail names none, the Date, and a Message-ID made of the
// spool's identifier at the sender's domain — and returns once the mail is in
// the spool: durable, with a Dir, before Send returns.
//
// # How a mail is delivered
//
// Run consumes the spool, one mail at a time, on the goroutine that calls it.
// Each attempt runs under SendTimeout, with its AttemptValue in the context.
// An attempt that fails with attempts left PARKS the mail: its lease is
// extended by Backoff.Delay(attempt) and left to lapse, and the queue hands it
// back then, its delivery count — the count that decides the dead letter, and
// that survives a crash — incremented. The last attempt's failure is the
// queue's nack, and the queue dead-letters the mail with that failure.
//
// # Exactly once, and the one duplicate left
//
// A mail this spool delivered is remembered by its identifier (the last
// DeliveredMemory of them), so a redelivery — the lease lapsed while the relay
// was still accepting the mail — is dropped rather than sent. The lease is
// twice SendTimeout, so that needs a relay slower than the timeout. What
// remains is a process that dies between the relay's acceptance and the
// acknowledgement: the next process sends the mail again, with the SAME
// Message-ID, which is how a receiver recognises it. No transport offers more:
// SMTP has no idempotent submission.
package spool

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"time"

	coremail "github.com/kitsunium/sdk/internal/core/mail"
	corequeue "github.com/kitsunium/sdk/internal/core/queue"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
	svcid "github.com/kitsunium/sdk/internal/service/id"
	svcqueue "github.com/kitsunium/sdk/internal/service/queue"
	svcres "github.com/kitsunium/sdk/internal/service/resilience"
)

// spooledValue is one mail as the spool writes it into the queue.
type spooledValue struct {
	// QueuedAt is when Send queued it.
	QueuedAt time.Time `json:"queued_at"`
	// Meta is what Config.Annotate returned at Send.
	Meta map[string]string `json:"meta,omitempty"`
	// ID is the spool's identifier.
	ID string `json:"id"`
	// Message is the stamped mail.
	Message coremail.MessageValue `json:"message"`
}

// Spool is a durable outbox for mail. Build one with [New]; Send queues, Run
// delivers. It is safe for concurrent use.
type Spool struct {
	// transport delivers each mail.
	transport coremail.Transport
	// broker is the queue the spool writes into.
	broker corequeue.Broker
	// clock stamps, bounds and paces.
	clock clock.Timed
	// observe, annotate and newID are the configured functions.
	observe  func(EventValue)
	annotate func(ctx context.Context) map[string]string
	newID    func() (string, error)
	// delivered remembers the mails this spool delivered.
	delivered *ledger
	// from is the default sender.
	from coremail.AddressValue
	// backoff is the retry curve.
	backoff svcres.BackoffValue
	// announcing maps the identifier of a mail a Send published and has not
	// yet told the observer about to the channel that Send closes once it
	// has: a delivery of that mail waits for it before it reports anything.
	announcing map[string]chan struct{}
	// maxAttempts, sendTimeout and poll are the resolved limits.
	maxAttempts int
	sendTimeout time.Duration
	poll        time.Duration
	// observing serialises the observer's calls.
	observing sync.Mutex
	// mu guards closed and announcing.
	mu     sync.RWMutex
	closed bool
}

// New builds a spool from cfg over a durable queue in cfg.Dir, or an
// in-memory one without it. It starts nothing: Run delivers.
func New(cfg Config) (*Spool, error) {
	//: everything decidable before a queue exists.
	if invalid := cfg.validate(); invalid != nil {
		//: SpoolMisconfigured.
		return nil, invalid
	}
	s := resolve(&cfg)
	policy := corequeue.PolicyValue{
		VisibilityTimeout: 2 * s.sendTimeout,
		RetryDelay:        s.backoff.BaseDelay,
		MaxDeliveries:     cfg.MaxAttempts,
		MaxMessageBytes:   maxMessageBytes(cfg.MaxMessageBytes),
	}
	var brokerErr error
	//: durable with a directory, in memory without one.
	if cfg.Dir == "" {
		s.broker, brokerErr = svcqueue.NewMemory(svcqueue.MemoryConfig{Clock: s.clock, Policy: policy})
	} else {
		s.broker, brokerErr = svcqueue.NewFile(svcqueue.FileConfig{Clock: s.clock, Dir: cfg.Dir, Policy: policy})
	}
	//: the queue's own verdict — a directory it refuses, a platform it cannot
	//: serve.
	if brokerErr != nil {
		//: as the queue said it.
		return nil, brokerErr
	}
	//: ready to take mail.
	return s, nil
}

// resolve builds the spool's fields from cfg, applying the documented clamps.
func resolve(cfg *Config) *Spool {
	s := &Spool{
		transport: cfg.Transport, clock: cfg.Clock, observe: cfg.Observe, annotate: cfg.Annotate,
		newID: cfg.NewID, delivered: newLedger(DeliveredMemory), from: cfg.From, backoff: cfg.Backoff,
		announcing: map[string]chan struct{}{}, maxAttempts: cfg.MaxAttempts, sendTimeout: cfg.SendTimeout,
		poll: cfg.PollInterval,
	}
	//: the wall clock is the only non-arbitrary default.
	if s.clock == nil {
		s.clock = clock.System
	}
	//: a ULID: time-ordered, so a spool listing reads in queue order.
	if s.newID == nil {
		s.newID = svcid.ULID.New
	}
	//: a zero curve would redial a dead relay as fast as the queue leases.
	if s.backoff == (svcres.BackoffValue{}) {
		s.backoff = svcres.BackoffValue{BaseDelay: DefaultRetryBase, MaxDelay: DefaultRetryMax}
	}
	//: so would a curve without a base, whatever else it sets. The caller's
	//: ceiling, factor and jitter are kept.
	if s.backoff.BaseDelay <= 0 {
		s.backoff.BaseDelay = DefaultRetryBase
	}
	//: an unset bound on an attempt is the default one.
	if s.sendTimeout <= 0 {
		s.sendTimeout = DefaultSendTimeout
	}
	//: the spool, before its queue.
	return s
}

// maxMessageBytes resolves the bound on one spooled mail.
func maxMessageBytes(configured int) int {
	//: zero is the documented request for the default.
	if configured == 0 {
		//: the spool's own default, above the queue's.
		return DefaultMaxMessageBytes
	}
	//: the caller's bound.
	return configured
}

// Send validates msg, stamps what a retry must not change, and queues it. It
// returns the spool's identifier for the mail once the mail is in the spool —
// on disk, with a Dir. A mail without a sender gets Config.From; without a
// Date, the spool's clock; without a Message-ID, one made of its identifier
// at the sender's domain, which every attempt keeps.
//
// A refusal is the mail domain's own typed verdict (HeaderInjection,
// InvalidAddress, NoRecipients, EmptyBody…), the queue's (MessageTooLarge),
// SpoolMisconfigured for an empty identifier from Config.NewID, or
// SpoolClosed; nothing is queued.
func (s *Spool) Send(ctx context.Context, msg coremail.MessageValue) (id string, err error) {
	s.mu.RLock()
	closed := s.closed
	s.mu.RUnlock()
	//: a closed spool takes nothing.
	if closed {
		//: SpoolClosed.
		return "", kerrs.Wrap(SpoolClosed, kerrs.WrapParams{})
	}
	record, stampErr := s.stamp(ctx, msg)
	//: the identifier could not be minted, or the mail is refused.
	if stampErr != nil {
		//: nothing queued.
		return "", stampErr
	}
	payload, encodeErr := json.Marshal(record)
	//: a Date encoding/json refuses.
	if encodeErr != nil {
		//: MessageUnencodable; nothing queued.
		return "", kerrs.Wrap(MessageUnencodable, kerrs.WrapParams{}, kerrs.String("mail", record.ID))
	}
	//: the consumer may deliver the mail before Publish returns here: its
	//: delivery waits until the observer has heard it was queued.
	defer s.announce(record.ID)()
	//: durable before Send returns, with a Dir.
	if _, publishErr := s.broker.Publish(ctx, payload); publishErr != nil {
		//: MessageTooLarge, QueueBackendFailed, or the caller's context.
		return "", publishErr
	}
	s.emit(&EventValue{Kind: EventQueued, At: record.QueuedAt, QueuedAt: record.QueuedAt, ID: record.ID, Message: record.Message, Meta: record.Meta})
	//: the spool's identifier.
	return record.ID, nil
}

// announce registers id as a mail Send is publishing and has not yet told the
// observer about, and returns the function that ends the registration — once
// the observer was told, or once the publication failed. A spool nobody
// observes registers nothing: there is no order to keep.
func (s *Spool) announce(id string) (done func()) {
	//: no observer, no order to keep.
	if s.observe == nil {
		//: nothing to end.
		return func() {}
	}
	told := make(chan struct{})
	s.mu.Lock()
	s.announcing[id] = told
	s.mu.Unlock()
	//: the registration's end, which releases a delivery waiting for it.
	return func() {
		s.mu.Lock()
		//: a later Send under the same identifier owns the entry now.
		if s.announcing[id] == told {
			delete(s.announcing, id)
		}
		s.mu.Unlock()
		close(told)
	}
}

// awaitAnnouncement waits until the observer has heard that the mail id was
// queued, when the Send that queued it is still running in this process. A
// mail an earlier process queued has nothing to wait for.
func (s *Spool) awaitAnnouncement(id string) {
	s.mu.RLock()
	told := s.announcing[id]
	s.mu.RUnlock()
	//: a Send between its publication and its Queued event.
	if told != nil {
		<-told
	}
}

// stamp fills in what a retry must not change and validates the result.
func (s *Spool) stamp(ctx context.Context, msg coremail.MessageValue) (spooledValue, error) {
	//: the default sender, when the mail names none.
	if msg.From.IsZero() {
		msg.From = s.from
	}
	now := s.clock.Now().UTC()
	//: the Date every attempt carries.
	if msg.Date.IsZero() {
		msg.Date = now
	}
	id, idErr := s.newID()
	//: no identifier, no mail.
	if idErr != nil {
		//: the generator's own typed verdict.
		return spooledValue{}, idErr
	}
	//: an empty identifier is one every later empty one would repeat, and the
	//: spool drops a mail whose identifier it delivered already.
	if id == "" {
		//: SpoolMisconfigured, naming the setting.
		return spooledValue{}, misconfigured("NewID", "returned an empty identifier")
	}
	//: the Message-ID every attempt carries, so a receiver can tell a retry
	//: from a new mail.
	if msg.MessageID == "" && !msg.From.IsZero() {
		msg.MessageID = id + "@" + domainOf(msg.From.Addr)
	}
	//: everything the transport would refuse, refused here.
	if validErr := coremail.Validate(msg); validErr != nil {
		//: the mail domain's own verdict.
		return spooledValue{}, validErr
	}
	var meta map[string]string
	//: what the context knows that a delivery should.
	if s.annotate != nil {
		meta = s.annotate(ctx)
	}
	//: ready to be queued.
	return spooledValue{QueuedAt: now, Meta: meta, ID: id, Message: msg}, nil
}

// domainOf returns the domain of an addr-spec: what follows its last '@'.
func domainOf(addr string) string {
	//: an address the mail domain accepted always has one.
	if _, domain, found := strings.CutLast(addr, "@"); found {
		//: the domain.
		return domain
	}
	//: reserved for documentation by RFC 2606, and never anybody's.
	return "spool.invalid"
}

// Run delivers the spool until ctx ends, on the calling goroutine: it returns
// nil when ctx ends, and the queue's error when the spool's storage fails —
// a spool whose directory stopped answering is not something to retry in a
// loop, and a supervisor restarting Run is. A delivery's failure is never
// returned: it is retried, and then dead-lettered.
func (s *Spool) Run(ctx context.Context) error {
	//: one mail at a time, on the consumer's goroutine.
	return svcqueue.Consume(ctx, s.broker, svcqueue.ConsumerConfig{
		Handler:      s.deliver,
		Clock:        s.clock,
		PollInterval: s.poll,
		Parallelism:  1,
		BatchSize:    1,
		// A mail this spool delivered is never sent again, and every attempt
		// carries the same Message-ID: a redelivery after a crash is the one
		// duplicate left, and a receiver can recognise it.
		HandlerIsIdempotent: true,
	})
}

// DeadLetters returns up to max mails the spool gave up on, oldest first,
// with the last failure of each. It removes nothing: a dead letter is
// evidence.
func (s *Spool) DeadLetters(ctx context.Context, maxLetters int) ([]DeadLetterValue, error) {
	reader, readable := s.broker.(corequeue.DeadLetterReader)
	//: both queues the spool builds can read theirs.
	if !readable {
		//: nothing to read.
		return nil, nil
	}
	dead, err := reader.DeadLetters(ctx, maxLetters)
	//: InvalidBatchSize, QueueBackendFailed, or the caller's context.
	if err != nil {
		//: as the queue said it.
		return nil, err
	}
	out := make([]DeadLetterValue, len(dead))
	//: each record, decoded when it decodes.
	for i, letter := range dead {
		var record spooledValue
		//: a record that does not decode is still evidence: its queue
		//: identifier and its cause survive.
		if json.Unmarshal(letter.Message.Payload, &record) != nil {
			record = spooledValue{}
		}
		out[i] = DeadLetterValue{
			Meta: record.Meta, FailedAt: letter.FailedAt, QueuedAt: record.QueuedAt, ID: record.ID,
			QueueID: letter.Message.ID, Reason: letter.Reason, Cause: letter.Cause,
			Message: record.Message, Attempts: letter.Deliveries, Code: letter.Code,
		}
	}
	//: oldest first.
	return out, nil
}

// Close refuses every later Send and closes the spool's queue. Stop Run
// first — cancel its context and wait for it to return — or a delivery may be
// cut off mid-attempt; the mail it held comes back when its lease lapses.
func (s *Spool) Close() error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	closer, closable := s.broker.(io.Closer)
	//: a queue with nothing to release.
	if !closable {
		//: closed.
		return nil
	}
	//: the queue's own verdict.
	return closer.Close()
}

// emit tells the observer, when there is one, one call at a time.
func (s *Spool) emit(event *EventValue) {
	//: nobody asked.
	if s.observe == nil {
		return
	}
	s.observing.Lock()
	defer s.observing.Unlock()
	s.observe(*event)
}

// report tells the observer about a delivery, after the Queued event of the
// same mail: a mail is never sent, retried or dropped before it was queued,
// as far as the observer can tell.
func (s *Spool) report(event *EventValue) {
	s.awaitAnnouncement(event.ID)
	s.emit(event)
}
