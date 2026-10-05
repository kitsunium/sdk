package kit

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
)

// Messages at rest (ADR 0006 §4): a topic's messages and a queued command's
// inputs wait in queues, on disk with a data directory. Their members kit
// seals are sealed before they are queued and opened at their delivery,
// under the data key of the message's subject — for a queued command without
// one, of the user who dispatched it —, or under data-key itself when the
// message is about nobody. A box is bound to the topic or the command, to
// the message's own reference, and to the member's pointer. A member whose
// key is destroyed — its person erased —, or sealed under a version of
// data-key pruned since — a dead letter outliving it —, reads as its zero
// value. A queue in memory keeps nothing at rest, and seals nothing.

// sealedTopic is a topic's envelope as it rests in a subscription's queue
// when its message is sealed: the message as JSON, and the reference its
// boxes are bound to.
type sealedTopic struct {
	Trace string          `json:"trace,omitempty"`
	Ref   string          `json:"ref,omitempty"`
	Data  json.RawMessage `json:"data"`
}

// sealedQueued is a queued command's envelope as it rests in its queue when
// its input is sealed.
type sealedQueued struct {
	Trace string          `json:"trace,omitempty"`
	User  string          `json:"user,omitempty"`
	From  string          `json:"from,omitempty"`
	Key   string          `json:"key,omitempty"`
	Ref   string          `json:"ref,omitempty"`
	Input json.RawMessage `json:"input"`
}

// messageRef is a new message's own reference: 128 random bits, which its
// boxes are bound to, so that a box moved into another message does not
// open.
func messageRef() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// outbound is a message kit seals for a queue on disk: its value, of plan's
// type, the user who sent it, and the node and ref its boxes are bound to.
type outbound struct {
	plan      *classPlan
	v         reflect.Value
	who       string
	node, ref string
}

// sealMessage seals doc — the message m — for a queue on disk: its members
// kit seals are boxed under the data key of its subject, else of the user
// who sent it, else under data-key itself, bound to its node and ref.
func (a *App) sealMessage(ctx context.Context, m outbound, doc []byte) ([]byte, error) {
	plan, v, who, node, ref := m.plan, m.v, m.who, m.node, m.ref
	z, err := a.sealing(ctx)
	if err != nil {
		return nil, err
	}
	key := ""
	if id, ok := plan.subjectOf(v); ok && id != "" {
		key = z.refs.personRef(id)
	} else if who != "" {
		key = z.refs.personRef(who)
	}
	out, _, err := sealWalk(doc, plan.rules, "", false, func(at memberAt, value []byte) ([]byte, bool, error) {
		if isNull(value) {
			return value, false, nil
		}
		box, err := z.seal(ctx, key, value, node, ref, at.pointer)
		return box, false, err
	})
	return out, err
}

// openMessage opens the boxes of doc, a message of plan's type queued by
// node under ref: a member whose key is gone is left out, and reads as its
// zero value.
func (a *App) openMessage(ctx context.Context, plan *classPlan, node, ref string, doc []byte) ([]byte, error) {
	z, err := a.sealing(ctx)
	if err != nil {
		return nil, err
	}
	out, _, err := boxWalk(doc, plan.rules, "", false, func(at memberAt, value []byte) ([]byte, bool, error) {
		plain, err := z.open(ctx, value, node, ref, at.pointer)
		switch {
		case errors.Is(err, errErased):
			return nil, true, nil
		case err != nil:
			return nil, false, err
		}
		return plain, false, nil
	})
	return out, err
}

// sealsAtRest reports whether the topic's messages are sealed where they
// wait: its message has a member kit seals, and its subscriptions' queues
// are on disk.
func (t *TopicService[T]) sealsAtRest(a *App) bool {
	return a.sealsOnDisk() && planFor[T]().sealsAny()
}

// encode is the envelope a subscription's queue keeps: the message, and the
// publish's trace; its members kit seals sealed when the queues are on disk.
func (t *TopicService[T]) encode(ctx context.Context, a *App, header string, msg T) ([]byte, error) {
	if !t.sealsAtRest(a) {
		return json.Marshal(envelope[T]{Trace: header, Data: msg})
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	ref := messageRef()
	sealed, err := a.sealMessage(ctx, outbound{plan: planFor[T](), v: reflect.ValueOf(msg), node: t.id, ref: ref}, data)
	if err != nil {
		return nil, err
	}
	return json.Marshal(sealedTopic{Trace: header, Ref: ref, Data: sealed})
}

// decode is the message a subscription's queue delivered and the trace of
// its publish, its boxes opened: openErr says it could not be opened —
// kit's data keys failed —, decodeErr that it does not decode.
func (t *TopicService[T]) decode(ctx context.Context, a *App, payload []byte) (msg T, trace string, decodeErr, openErr error) {
	var env sealedTopic
	if decodeErr = json.Unmarshal(payload, &env); decodeErr != nil {
		return msg, "", decodeErr, nil
	}
	data := env.Data
	if hasBoxes(data) {
		if data, openErr = a.openMessage(ctx, planFor[T](), t.id, env.Ref, data); openErr != nil {
			return msg, env.Trace, nil, openErr
		}
	}
	if len(data) > 0 {
		decodeErr = json.Unmarshal(data, &msg)
	}
	return msg, env.Trace, decodeErr, nil
}

// sealsAtRest reports whether the command's dispatches are sealed where
// they wait: it is queued, its input has a member kit seals, and its queue
// is on disk.
func (c *Command[C, R]) sealsAtRest(a *App) bool {
	return c.opts.queued && a.sealsOnDisk() && planFor[C]().sealsAny()
}

// encode is what the command's queue keeps of a dispatch: its envelope, the
// input's members kit seals sealed — under its subject's key, else the
// dispatcher's — when the queue is on disk.
func (c *Command[C, R]) encode(ctx context.Context, a *App, env queuedEnvelope[C]) ([]byte, error) {
	if !c.sealsAtRest(a) {
		return json.Marshal(env)
	}
	input, err := json.Marshal(env.Input)
	if err != nil {
		return nil, err
	}
	ref := messageRef()
	sealed, err := a.sealMessage(ctx, outbound{plan: planFor[C](), v: reflect.ValueOf(env.Input), who: env.User, node: c.id, ref: ref}, input)
	if err != nil {
		return nil, err
	}
	return json.Marshal(sealedQueued{Trace: env.Trace, User: env.User, From: env.From, Key: env.Key, Ref: ref, Input: sealed})
}

// decode is a dispatch its queue delivered, its input's boxes opened:
// openErr says it could not be opened, decodeErr that it does not decode.
func (c *Command[C, R]) decode(ctx context.Context, a *App, payload []byte) (env queuedEnvelope[C], decodeErr, openErr error) {
	var raw sealedQueued
	if decodeErr = json.Unmarshal(payload, &raw); decodeErr != nil {
		return env, decodeErr, nil
	}
	env = queuedEnvelope[C]{Trace: raw.Trace, User: raw.User, From: raw.From, Key: raw.Key}
	input := raw.Input
	if hasBoxes(input) {
		if input, openErr = a.openMessage(ctx, planFor[C](), c.id, raw.Ref, input); openErr != nil {
			return env, nil, openErr
		}
	}
	if len(input) > 0 {
		decodeErr = json.Unmarshal(input, &env.Input)
	}
	return env, decodeErr, nil
}
