package app

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/data/queue"
)

// What `queue` does with a consumer's dead letters (issue #1138), on the
// watch of the keys world, its queue a file under the data directory and a
// table of the fake database: listed with its consumer, ID, reason and size
// and never its payload; replayed back into the live queue, its attempts
// counted afresh; dropped for good; a second act on one answers not found
// and exits 1; a consumer the product does not mount exits 2 and names the
// ones it does.

// queueCmd runs the queue command on an app of w's place that is not
// running, and returns its status and streams.
func queueCmd(t *testing.T, w *keysWorld, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = w.app(nil).queueCommand(t.Context(), args, &out, &errOut)
	return code, out.String(), errOut.String()
}

// plantDeadLetters writes payloads as dead letters into the watch's queue, as
// the start opens it, and returns their identifiers.
func plantDeadLetters(t *testing.T, w *keysWorld, payloads ...string) []string {
	t.Helper()
	ctx := t.Context()
	app := w.app(nil)
	must(t, app.resolve())
	app.queueSettingsRefused(&bytes.Buffer{})
	must(t, app.mount())
	defer app.unmount()
	var ids []string
	err := app.withConsumerQueues(ctx, []queueOwner{w.watch}, func(queues []consumerQueue) error {
		if queues[0].broker == nil {
			t.Fatal("the watch has no queue after its start")
		}
		writer := sdkQueue(t, queues[0].broker).(queue.DeadLetterWriter)
		for _, payload := range payloads {
			id, err := writer.PutDeadLetter(ctx, queue.DeadLetter{
				Message:  queue.Message{Payload: []byte(payload), EnqueuedAt: time.Now()},
				FailedAt: time.Now(), Deliveries: 5, Reason: "WATCH_FAILED", Cause: "the handler failed",
			})
			if err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return nil
	})
	must(t, err)
	return ids
}

// liveOfWatch are the payloads of the live messages of the watch's queue.
func liveOfWatch(t *testing.T, w *keysWorld) []string {
	t.Helper()
	app := w.app(nil)
	must(t, app.resolve())
	app.queueSettingsRefused(&bytes.Buffer{})
	must(t, app.mount())
	defer app.unmount()
	var out []string
	must(t, app.withConsumerQueues(context.Background(), []queueOwner{w.watch}, func(queues []consumerQueue) error {
		live, err := sdkQueue(t, queues[0].broker).(queue.MessageReader).Messages(t.Context(), "", 100)
		for _, m := range live {
			if m.Deliveries != 0 {
				t.Errorf("a replayed message counts %d deliveries, want them counted afresh", m.Deliveries)
			}
			out = append(out, string(m.Message.Payload))
		}
		return err
	}))
	return out
}

func TestTheQueueCommandListsReplaysAndDropsAConsumersDeadLetters(t *testing.T) {
	for name, place := range keysEngines(t) {
		t.Run(name, func(t *testing.T) {
			w := newKeysWorld(t, place)
			stop(t, w.start(t, nil))
			ids := plantDeadLetters(t, w, "first secret payload", "second secret payload")

			code, out, errOut := queueCmd(t, w, "dead-letters")
			if code != 0 || strings.Count(out, "\n") != 2 || !strings.Contains(out, w.watch.id+"\t"+ids[0]+"\t") ||
				!strings.Contains(out, "WATCH_FAILED\t5 attempts\t20 bytes\tthe handler failed") || strings.Contains(out, "secret") {
				t.Fatalf("queue dead-letters = %d %q %q; want two lines naming the watch, never a payload", code, out, errOut)
			}
			if code, out, _ := queueCmd(t, w, "dead-letters", w.watch.id); code != 0 || strings.Count(out, "\n") != 2 {
				t.Fatalf("queue dead-letters %s = %d %q; want its two", w.watch.id, code, out)
			}

			code, out, errOut = queueCmd(t, w, "replay", w.watch.id, ids[0])
			if want := say("queue.replayed", "count", 1, "node", w.watch.id).String() + "\n"; code != 0 || out != want {
				t.Fatalf("queue replay = %d %q %q; want 0 %q", code, out, errOut, want)
			}
			if live := liveOfWatch(t, w); len(live) != 1 || live[0] != "first secret payload" {
				t.Fatalf("the watch's live queue after the replay = %q; want the replayed message", live)
			}
			if code, _, errOut := queueCmd(t, w, "replay", w.watch.id, ids[0]); code != 1 || !strings.Contains(errOut, "no dead letter "+ids[0]) {
				t.Fatalf("a second replay = %d %q; want 1 and not found", code, errOut)
			}

			if code, out, _ := queueCmd(t, w, "drop", w.watch.id, ids[1]); code != 0 || !strings.Contains(out, "dropped 1 dead letter(s)") {
				t.Fatalf("queue drop = %d %q; want 0 and one dropped", code, out)
			}
			if code, _, errOut := queueCmd(t, w, "drop", w.watch.id, ids[1]); code != 1 || !strings.Contains(errOut, "no dead letter "+ids[1]) {
				t.Fatalf("a second drop = %d %q; want 1 and not found", code, errOut)
			}
			if code, out, _ := queueCmd(t, w, "dead-letters"); code != 0 || !strings.Contains(out, "no dead letter in the consumers' queues") {
				t.Fatalf("queue dead-letters after both = %d %q; want none", code, out)
			}

			if code, _, errOut := queueCmd(t, w, "dead-letters", "forum/store/nothing"); code != 2 ||
				!strings.Contains(errOut, "no consumer forum/store/nothing") || !strings.Contains(errOut, w.watch.id) {
				t.Fatalf("queue dead-letters of no consumer = %d %q; want 2 naming the consumers", code, errOut)
			}
			for _, args := range [][]string{{}, {"replay", w.watch.id}, {"drop"}, {"dead-letters", "a", "b"}, {"replay", w.watch.id, "--all"}} {
				if code, _, errOut := queueCmd(t, w, args...); code != 2 || !strings.Contains(errOut, "forum queue replay NODE ID") {
					t.Errorf("queue %q = %d %q; want 2 and the usage", args, code, errOut)
				}
			}
		})
	}
}

func TestTheQueueCommandRefusesQueuesKeptInMemory(t *testing.T) {
	w := newKeysWorld(t, keysPlace{opts: []AppConfigurer{InMemory()}})
	code, _, errOut := queueCmd(t, w, "dead-letters")
	if code != 1 || !strings.Contains(errOut, "keeps its queues in memory") {
		t.Fatalf("queue dead-letters in memory = %d %q; want 1 and the refusal", code, errOut)
	}
}
