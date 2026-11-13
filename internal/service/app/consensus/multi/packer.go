package multi

import (
	"context"
	"slices"
	"sync"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// The defaults a zero bound of Config clamps to.
const (
	// DefaultMaxCommands is the commands one entry carries when
	// Config.MaxCommands is zero.
	DefaultMaxCommands int = 4096
	// DefaultMaxBytes is the largest packed body when Config.MaxBytes is zero:
	// the engine's default MaxEntryBytes.
	DefaultMaxBytes int = 1 << 20
	// DefaultMaxInFlight is the packed entries in flight at once when
	// Config.MaxInFlight is zero: one, so every command proposed while an
	// entry is in flight waits for it and leaves in the next.
	DefaultMaxInFlight int = 1
)

// Config bounds what a Packer packs into one entry. A zero bound takes its
// default.
type Config struct {
	// MaxCommands is the most commands one entry carries.
	MaxCommands int
	// MaxBytes is the largest packed body; it must not exceed the node's
	// MaxEntryBytes, or a full entry is refused ENTRY_TOO_LARGE.
	MaxBytes int
	// MaxInFlight is the most packed entries proposed and not yet answered;
	// the commands proposed while every one is in flight leave together in
	// the next.
	MaxInFlight int
}

// ResultValue is what a Packer answers one caller: the token of the entry its
// command was packed into, the command's index in that entry, and the result
// Inner returned for it.
type ResultValue struct {
	// Token is the packed entry's token, shared by every command in it.
	Token coreconsensus.TokenValue
	// Sub is the command's index in the entry, in packing order.
	Sub int
	// Value is the result Inner's ApplyBatch wrote for the command.
	Value any
}

// request is one caller's command, queued until the Packer takes it.
type request struct {
	cmd   []byte
	taken bool
	done  chan outcome
}

// outcome is the answer to one request.
type outcome struct {
	res ResultValue
	err error
}

// Packer packs the commands its callers propose at one moment into one entry
// and proposes it with ONE Propose, then answers each caller with its own
// result. There is no linger timer (ADR 0152 D6): a command proposed while
// fewer than MaxInFlight entries are in flight leaves at once, and those
// proposed while every one is in flight leave together in the next. The
// node's state machine must be a Machine. A Packer is safe for
// concurrent use; Close stops it.
type Packer struct {
	r   coreconsensus.Replicated
	cfg Config

	mu     sync.Mutex
	queue  []*request
	closed bool
	// stopped closes stop once, whichever Close comes first.
	stopped sync.Once

	kick   chan struct{}
	slots  chan struct{}
	flight sync.WaitGroup
	stop   chan struct{}
	done   chan struct{}
	life   context.Context
	cancel context.CancelFunc
}

// NewPacker starts a Packer proposing through r, bounded by cfg.
func NewPacker(r coreconsensus.Replicated, cfg Config) *Packer {
	if cfg.MaxCommands <= 0 {
		cfg.MaxCommands = DefaultMaxCommands
	}
	if cfg.MaxBytes <= 0 {
		cfg.MaxBytes = DefaultMaxBytes
	}
	if cfg.MaxInFlight <= 0 {
		cfg.MaxInFlight = DefaultMaxInFlight
	}
	return startPacker(r, cfg)
}

// startPacker builds the Packer of NewPacker over cfg, its bounds already
// defaulted, and launches its loop goroutine. The loop runs until Close
// closes stop, then waits for the entries in flight, answers what is still
// queued NODE_STOPPED and closes done, which Close waits for: no goroutine
// of the Packer outlives a Close that returned.
func startPacker(r coreconsensus.Replicated, cfg Config) *Packer {
	life, cancel := context.WithCancel(context.Background())
	p := &Packer{
		r: r, cfg: cfg,
		kick:  make(chan struct{}, 1),
		slots: make(chan struct{}, cfg.MaxInFlight),
		stop:  make(chan struct{}),
		done:  make(chan struct{}),
		life:  life, cancel: cancel,
	}
	go p.loop()
	return p
}

// Propose packs cmd with the commands proposed beside it and returns once
// their entry is applied locally, with the entry's token, the command's index
// in it and its own result. A command whose packed body would exceed MaxBytes
// alone is ENTRY_TOO_LARGE. A ctx that ends before the command is packed
// answers ctx.Err(), the command never proposed; once it is packed, the
// answer is OUTCOME_UNKNOWN, as Propose's. The errors of the entry's Propose
// reach every caller packed in it; a result that is not one per command is
// RESULT_TYPE_MISMATCH, the entry committed and its token returned. A Packer
// closed answers NODE_STOPPED. The caller may reuse cmd once Propose returns.
func (p *Packer) Propose(ctx context.Context, cmd []byte) (ResultValue, error) {
	if bodyHeaderMax+cmdCost(cmd) > p.cfg.MaxBytes {
		return ResultValue{}, origin(coreconsensus.EntryTooLarge, errs.Int("bytes", len(cmd)))
	}
	if err := ctx.Err(); err != nil {
		return ResultValue{}, err
	}
	r := &request{cmd: cmd, done: make(chan outcome, 1)}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return ResultValue{}, origin(coreconsensus.NodeStopped, errs.String("packer", "closed"))
	}
	p.queue = append(p.queue, r)
	p.mu.Unlock()
	select {
	case p.kick <- struct{}{}:
	default:
	}
	select {
	case o := <-r.done:
		return o.res, o.err
	case <-ctx.Done():
		return p.abandon(ctx, r)
	}
}

// abandon answers a caller whose ctx ended: a command still queued is taken
// out and never proposed; one already packed may still commit.
func (p *Packer) abandon(ctx context.Context, r *request) (ResultValue, error) {
	p.mu.Lock()
	if !r.taken {
		if i := slices.Index(p.queue, r); i >= 0 {
			p.queue = slices.Delete(p.queue, i, i+1)
		}
		p.mu.Unlock()
		return ResultValue{}, ctx.Err()
	}
	p.mu.Unlock()
	return ResultValue{}, errs.Wrap(ctx.Err(), coreconsensus.OutcomeUnknown.Params())
}

// Close stops the Packer: a command still queued is answered NODE_STOPPED,
// the entries in flight are waited for within ctx. When ctx ends first, their
// Proposes are cancelled, their callers answered OUTCOME_UNKNOWN, and
// Close returns OUTCOME_UNKNOWN. A second Close waits for the first.
func (p *Packer) Close(ctx context.Context) error {
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
	p.stopped.Do(func() { close(p.stop) })
	defer p.cancel()
	select {
	case <-p.done:
		return nil
	case <-ctx.Done():
	}
	p.cancel()
	<-p.done
	return errs.Wrap(ctx.Err(), coreconsensus.OutcomeUnknown.Params())
}

// loop proposes the queued commands, each packed entry on a goroutine of its
// own and at most MaxInFlight at once, until Close; then it waits for those
// in flight and answers what is left NODE_STOPPED.
func (p *Packer) loop() {
	defer close(p.done)
	defer p.flight.Wait()
	for {
		select {
		case <-p.kick:
		case <-p.stop:
			p.failQueued()
			return
		}
		for {
			select {
			case p.slots <- struct{}{}:
			case <-p.stop:
				p.failQueued()
				return
			}
			batch, body := p.take()
			if len(batch) == 0 {
				<-p.slots
				break
			}
			p.flight.Add(1)
			go p.send(batch, body)
		}
	}
}

// send proposes one packed entry, answers its callers and frees its slot.
func (p *Packer) send(batch []*request, body []byte) {
	defer p.flight.Done()
	tok, val, err := p.r.Propose(p.life, body)
	// The entry's result is the Machine's: an error for a body that
	// did not parse, else one result per command. A result of any other
	// type is left nil, which answer refuses as it refuses a wrong count,
	// since a batch holds one request at least (take).
	if verr, ok := val.(error); ok && err == nil {
		err = verr
	}
	subs, _ := val.([]any)
	answer(batch, tok, subs, err)
	<-p.slots
}

// take packs the head of the queue, within MaxCommands and MaxBytes, into a
// fresh body, and marks its requests taken: from here their callers can no
// longer withdraw them, and their buffers are no longer read.
func (p *Packer) take() ([]*request, []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	n, size := 0, bodyHeaderMax
	for n < len(p.queue) && n < p.cfg.MaxCommands {
		c := cmdCost(p.queue[n].cmd)
		if n > 0 && size+c > p.cfg.MaxBytes {
			break
		}
		size += c
		n++
	}
	if n == 0 {
		return nil, nil
	}
	batch := slices.Clone(p.queue[:n])
	p.queue = slices.Delete(p.queue, 0, n)
	cmds := make([][]byte, n)
	for i, r := range batch {
		r.taken = true
		cmds[i] = r.cmd
	}
	return batch, appendBody(make([]byte, 0, size), cmds)
}

// answer hands each request of batch its sub-result of subs, or err.
func answer(batch []*request, tok coreconsensus.TokenValue, subs []any, err error) {
	if err != nil {
		for _, r := range batch {
			r.done <- outcome{err: err}
		}
		return
	}
	if len(subs) != len(batch) {
		mismatch := origin(coreconsensus.ResultTypeMismatch, errs.Int("commands", len(batch)))
		for i, r := range batch {
			r.done <- outcome{res: ResultValue{Token: tok, Sub: i}, err: mismatch}
		}
		return
	}
	for i, r := range batch {
		r.done <- outcome{res: ResultValue{Token: tok, Sub: i, Value: subs[i]}}
	}
}

// failQueued answers every queued request NODE_STOPPED. Close set closed
// before it signalled stop, so nothing joins the queue after.
func (p *Packer) failQueued() {
	p.mu.Lock()
	q := p.queue
	p.queue = nil
	p.mu.Unlock()
	err := origin(coreconsensus.NodeStopped, errs.String("packer", "closed"))
	for _, r := range q {
		r.done <- outcome{err: err}
	}
}
