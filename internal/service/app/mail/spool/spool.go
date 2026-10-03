// Package spool is outbound mail made durable: Send validates a mail and
// queues it, and the spool's consumer hands it to a mail transport, retrying a
// failure with a backoff and dead-lettering the mail after its last attempt. A
// redelivery of a mail it delivered is dropped, and every attempt, the one
// resend a crash can cause included, carries the same Message-ID. ADR 0111.
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
// # An identifier the caller minted
//
// Send mints the mail's identifier with Config.NewID. SendWithID queues the
// mail under one its caller minted instead, for a caller that must know the
// identifier before the spool has the mail — a framework that holds a mail
// until a transaction commits returns the identifier at the call and queues
// the mail after the commit. Every identifier, whoever minted it, is
// non-empty, at most MaxIDBytes and an RFC 5322 dot-atom, because it becomes
// the left half of the mail's Message-ID: a caller's that is not is
// InvalidMailID, a generator's SpoolMisconfigured. A repeated identifier is
// not refused. The spool cannot see the identifiers in its queue without
// reading every record; it drops a mail under an identifier it delivered, as
// it drops every redelivery.
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

	coremail "github.com/kitsunium/sdk/internal/core/app/mail"
	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
	kbackoff "github.com/kitsunium/sdk/internal/kernel/backoff"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
	svcid "github.com/kitsunium/sdk/internal/service/app/id"
	svcqueue "github.com/kitsunium/sdk/internal/service/data/queue"
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

// announcement is the Sends of one identifier that published their mail and
// have not yet told the observer. Nothing stops an identifier from repeating,
// so there may be several at once, and a delivery of that identifier waits
// for the last of them.
type announcement struct {
	// told is closed once pending falls to zero.
	told chan struct{}
	// pending counts them; the spool's mu guards it.
	pending int
}

// Spool is a durable outbox for mail. Build one with [New]; Send and
// SendWithID queue, Run delivers. It is safe for concurrent use.
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
	backoff kbackoff.Value
	// announcing maps the identifier of a mail a Send published and has not
	// yet told the observer about to the Sends of that identifier still in
	// that window: a delivery of that identifier waits until none is before
	// it reports anything.
	announcing map[string]*announcement
	// maxAttempts, sendTimeout and poll are the resolved limits.
	maxAttempts int
	sendTimeout time.Duration
	poll        time.Duration
	// observing serialises the observer's calls.
	observing sync.Mutex
	// closing guards closed and the queue's lifetime: every publication
	// holds it for reading, and Close for writing, so Close waits for a
	// publication in flight and none starts after it.
	closing sync.RWMutex
	// mu guards announcing.
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
		announcing: map[string]*announcement{}, maxAttempts: cfg.MaxAttempts, sendTimeout: cfg.SendTimeout,
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
	if s.backoff == (kbackoff.Value{}) {
		s.backoff = kbackoff.Value{BaseDelay: DefaultRetryBase, MaxDelay: DefaultRetryMax}
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

// Send validates msg, stamps what a retry must not change, and queues it
// under an identifier Config.NewID mints. It returns that identifier once the
// mail is in the spool — on disk, with a Dir. A mail without a sender gets
// Config.From; without a Date, the spool's clock; without a Message-ID, one
// made of its identifier at the sender's domain, which every attempt keeps.
//
// A refusal is the mail domain's own typed verdict (HeaderInjection,
// InvalidAddress, NoRecipients, EmptyBody…), the queue's (MessageTooLarge),
// SpoolMisconfigured for an identifier from Config.NewID that breaks the rule
// SendWithID states, or SpoolClosed; nothing is queued. A Send racing Close
// either lands before the queue closes or is SpoolClosed.
func (s *Spool) Send(ctx context.Context, msg coremail.MessageValue) (id string, err error) {
	//: a closed spool takes nothing, and mints nothing for it.
	if s.isClosed() {
		//: SpoolClosed.
		return "", kerrs.Wrap(SpoolClosed, kerrs.WrapParams{})
	}
	id, mintErr := s.mint()
	//: no identifier, no mail.
	if mintErr != nil {
		//: the generator's own verdict, or SpoolMisconfigured.
		return "", mintErr
	}
	//: queued under it, or refused with nothing queued.
	if queueErr := s.queue(ctx, id, msg); queueErr != nil {
		//: as queue refused it.
		return "", queueErr
	}
	//: the spool's identifier.
	return id, nil
}

// SendWithID is Send under an identifier its caller minted, for a caller that
// must know the identifier before the spool has the mail: a framework that
// holds a mail until its transaction commits returns the identifier at the
// call and queues the mail after the commit. SendWithID stamps what Send
// stamps, the Message-ID it makes is id at the sender's domain, and the
// events, the attempts and the dead letter carry id.
//
// The identifier must be non-empty, at most MaxIDBytes, and an RFC 5322
// dot-atom — the grammar of a Message-ID's left half: printable ASCII, no
// special, no empty label — even when the mail brings its own Message-ID,
// because the rule belongs to the identifier and not to one mail. Anything
// else is InvalidMailID, which names the rule it broke and never the
// identifier; nothing is queued. Identifiers are compared byte for byte.
//
// The identifier is the caller's promise that the mail is new, as one from
// Config.NewID is. The spool does not look for it among the mails it holds;
// a repeated identifier meets the ledger every redelivery meets. A mail under
// an identifier this process delivered — among the last DeliveredMemory — is
// dropped at delivery with EventDuplicate, after its own EventQueued, instead
// of being sent. So SendWithID repeated after a failure that hid a landed
// publication sends the mail once, while the process remembers it. An
// identifier whose mail was dead-lettered was never delivered, and carries a
// new attempt. After a restart the ledger is empty, and a repeat is sent
// again — under the same Message-ID, when that was made of id.
//
// Every other refusal is Send's, and a SendWithID racing Close lands or is
// SpoolClosed.
func (s *Spool) SendWithID(ctx context.Context, id string, msg coremail.MessageValue) error {
	//: a closed spool takes nothing.
	if s.isClosed() {
		//: SpoolClosed.
		return kerrs.Wrap(SpoolClosed, kerrs.WrapParams{})
	}
	//: the rule every identifier keeps, whoever minted it.
	if problem, ok := checkID(id); !ok {
		//: InvalidMailID, naming the rule and the length — never the
		//: identifier, which may carry the very bytes it was refused for.
		return kerrs.Wrap(InvalidMailID, kerrs.WrapParams{},
			kerrs.String("problem", problem), kerrs.Int("bytes", len(id)))
	}
	//: queued under it, or refused with nothing queued.
	return s.queue(ctx, id, msg)
}

// mint asks Config.NewID for an identifier and holds it to the rule
// SendWithID holds a caller's to: one the generator got wrong is the
// configuration's defect, not the mail's.
func (s *Spool) mint() (string, error) {
	id, idErr := s.newID()
	//: the generator failed.
	if idErr != nil {
		//: its own typed verdict.
		return "", idErr
	}
	//: an identifier no mail can keep — an empty one, which every later
	//: empty one would repeat, among them.
	if problem, ok := checkID(id); !ok {
		//: SpoolMisconfigured, naming the setting.
		return "", misconfigured("NewID", "returned an identifier that is "+problem)
	}
	//: minted.
	return id, nil
}

// queue stamps msg under id, writes it into the spool and tells the observer
// it was queued. When it returns an error, nothing was queued.
func (s *Spool) queue(ctx context.Context, id string, msg coremail.MessageValue) error {
	record, stampErr := s.stamp(ctx, id, msg)
	//: the mail is refused.
	if stampErr != nil {
		//: the mail domain's own verdict.
		return stampErr
	}
	payload, encodeErr := json.Marshal(record)
	//: a Date encoding/json refuses.
	if encodeErr != nil {
		//: MessageUnencodable.
		return kerrs.Wrap(MessageUnencodable, kerrs.WrapParams{}, kerrs.String("mail", record.ID))
	}
	//: the consumer may deliver the mail before Publish returns here: its
	//: delivery waits until the observer has heard it was queued.
	defer s.announce(record.ID)()
	//: durable before the caller hears of it, with a Dir.
	if publishErr := s.publish(ctx, payload); publishErr != nil {
		//: SpoolClosed, MessageTooLarge, QueueBackendFailed, or the caller's
		//: context.
		return publishErr
	}
	s.emit(&EventValue{Kind: EventQueued, At: record.QueuedAt, QueuedAt: record.QueuedAt, ID: record.ID, Message: record.Message, Meta: record.Meta})
	//: in the spool.
	return nil
}

// isClosed reports whether Close ran.
func (s *Spool) isClosed() bool {
	s.closing.RLock()
	defer s.closing.RUnlock()
	//: as Close left it.
	return s.closed
}

// publish writes payload into the queue, unless the spool closed meanwhile.
// It holds closing for reading throughout, so Close waits for it and never
// closes the queue under it; the observer is told afterwards, outside it.
func (s *Spool) publish(ctx context.Context, payload []byte) error {
	s.closing.RLock()
	defer s.closing.RUnlock()
	//: Close won the race.
	if s.closed {
		//: SpoolClosed.
		return kerrs.Wrap(SpoolClosed, kerrs.WrapParams{})
	}
	_, publishErr := s.broker.Publish(ctx, payload)
	//: nil once the mail is in the queue; the queue's verdict otherwise.
	return publishErr
}

// announce registers a Send of id that is publishing its mail and has not yet
// told the observer, and returns the function that ends the registration —
// once the observer was told, or once the publication failed. The Sends of
// one identifier are counted, not replaced: an identifier can repeat, and the
// Send that tells the observer first must not release a delivery another has
// not told it about yet. A spool nobody observes registers nothing: there is
// no order to keep.
func (s *Spool) announce(id string) (done func()) {
	//: no observer, no order to keep.
	if s.observe == nil {
		//: nothing to end.
		return func() {}
	}
	s.mu.Lock()
	entry := s.announcing[id]
	//: the first Send of this identifier in the window.
	if entry == nil {
		entry = &announcement{told: make(chan struct{})}
		s.announcing[id] = entry
	}
	entry.pending++
	told := entry.told
	s.mu.Unlock()
	//: the registration's end, which releases a delivery waiting for the last.
	return func() {
		s.mu.Lock()
		entry.pending--
		last := entry.pending == 0
		//: no Send of this identifier is left in the window.
		if last {
			delete(s.announcing, id)
		}
		s.mu.Unlock()
		//: exactly one registration sees the count fall to zero, under mu,
		//: so told is closed once; a waiter reads the entry under mu and waits
		//: outside it.
		if last {
			close(told)
		}
	}
}

// awaitAnnouncement waits until the observer has heard that the mail id was
// queued, when a Send that queued it is still running in this process: until
// no Send of id is left between its publication and its Queued event. A mail
// an earlier process queued has nothing to wait for.
func (s *Spool) awaitAnnouncement(id string) {
	s.mu.RLock()
	entry := s.announcing[id]
	s.mu.RUnlock()
	//: Sends between their publication and their Queued event.
	if entry != nil {
		<-entry.told
	}
}

// stamp fills in what a retry must not change under id and validates the
// result.
func (s *Spool) stamp(ctx context.Context, id string, msg coremail.MessageValue) (spooledValue, error) {
	//: the default sender, when the mail names none.
	if msg.From.IsZero() {
		msg.From = s.from
	}
	now := s.clock.Now().UTC()
	//: the Date every attempt carries.
	if msg.Date.IsZero() {
		msg.Date = now
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

// Close refuses every later Send and closes the spool's queue, once the
// publications in flight have landed. Closing twice is closing once. Stop Run
// first — cancel its context and wait for it to return — or a delivery may be
// cut off mid-attempt; the mail it held comes back when its lease lapses.
func (s *Spool) Close() error {
	s.closing.Lock()
	defer s.closing.Unlock()
	//: the queue is closed already, and a second close of it would fail.
	if s.closed {
		//: closed.
		return nil
	}
	s.closed = true
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
