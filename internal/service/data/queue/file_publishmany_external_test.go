package queue_test

import (
	"io"
	"io/fs"
	"slices"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"

	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
	corevfs "github.com/kitsunium/sdk/internal/core/data/vfs"
	svcqueue "github.com/kitsunium/sdk/internal/service/data/queue"
)

// The file broker's PublishMany publishes its batch through vfs's
// AtomicBatchWriter, one directory flush for the whole batch (issue #827).
// The publisher is wrapped to count the publications and to fail a batch at
// a chosen message; vfs's own suite proves the flush counts and the prefix on
// the device (publish_internal_test.go).

// countingPublisher counts the publications a broker makes, and fails a
// batch at the message failAt when it is not negative.
type countingPublisher struct {
	inner   corevfs.AtomicWriter
	single  int
	batches int
	failAt  int
}

// WriteAtomic counts one publication and makes it.
func (c *countingPublisher) WriteAtomic(name string, data []byte, perm fs.FileMode) error {
	c.single++
	return c.inner.WriteAtomic(name, data, perm)
}

// WriteAtomicAll counts one batch, and publishes the files before failAt
// before failing.
func (c *countingPublisher) WriteAtomicAll(files []corevfs.FileValue, perm fs.FileMode) (int, error) {
	c.batches++
	batcher := c.inner.(corevfs.AtomicBatchWriter)
	if c.failAt < 0 || c.failAt >= len(files) {
		return batcher.WriteAtomicAll(files, perm)
	}
	published, err := batcher.WriteAtomicAll(files[:c.failAt], perm)
	if err != nil {
		return published, err
	}
	return published, corevfs.PublishFailed
}

// Close releases the wrapped publisher's descriptor.
func (c *countingPublisher) Close() error {
	if closer, ok := c.inner.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}

// counted wraps the broker's publisher in a countingPublisher.
func counted(t *testing.T, broker corequeue.Broker, failAt int, batchCapable bool) *countingPublisher {
	t.Helper()
	var wrapped *countingPublisher
	svcqueue.WrapPublisherForTest(broker, func(inner corevfs.AtomicWriter) corevfs.AtomicWriter {
		wrapped = &countingPublisher{inner: inner, failAt: failAt}
		if !batchCapable {
			return onlyWriteAtomic{wrapped}
		}
		return wrapped
	})
	return wrapped
}

// onlyWriteAtomic exposes WriteAtomic alone.
type onlyWriteAtomic struct {
	publisher *countingPublisher
}

// WriteAtomic forwards.
func (o onlyWriteAtomic) WriteAtomic(name string, data []byte, perm fs.FileMode) error {
	return o.publisher.WriteAtomic(name, data, perm)
}

// sixteen is a batch of sixteen distinct payloads.
func sixteen() [][]byte {
	payloads := make([][]byte, 16)
	for index := range payloads {
		payloads[index] = []byte{byte('a' + index)}
	}
	return payloads
}

// TestFilePublishManySharesOneDirectoryFlush pins issue #827 on the broker:
// a batch of sixteen is ONE batched publication, no single one, and every
// message is then receivable.
func TestFilePublishManySharesOneDirectoryFlush(t *testing.T) {
	t.Parallel()
	broker := makeFile(t, clock.NewManualClock(epoch), defaultPolicy())
	publisher := counted(t, broker, -1, true)
	messages, err := broker.(corequeue.BatchPublisher).PublishMany(t.Context(), sixteen())
	if err != nil || len(messages) != 16 {
		t.Fatalf("PublishMany(16) = %d, %v, want 16, nil", len(messages), err)
	}
	if publisher.batches != 1 || publisher.single != 0 {
		t.Fatalf("PublishMany(16) made %d batched and %d single publications, want 1 and 0", publisher.batches, publisher.single)
	}
	if got := receive(t, broker, 32); len(got) != 16 {
		t.Fatalf("Receive after PublishMany(16) = %d, want 16", len(got))
	}
}

// TestFilePublishManyReturnsTheDurablePrefix pins the partial failure: a
// batch the publisher fails at its third message returns the first two
// beside QUEUE_BACKEND_FAILED, and those two — exactly — are receivable.
func TestFilePublishManyReturnsTheDurablePrefix(t *testing.T) {
	t.Parallel()
	broker := makeFile(t, clock.NewManualClock(epoch), defaultPolicy())
	counted(t, broker, 2, true)
	messages, err := broker.(corequeue.BatchPublisher).PublishMany(t.Context(), sixteen()[:4])
	if !errs.HasCode(err, corequeue.CodeQueueBackendFailed) || len(messages) != 2 {
		t.Fatalf("PublishMany failing at the third = %d, %v, want 2 and QUEUE_BACKEND_FAILED", len(messages), err)
	}
	var got []string
	for _, delivery := range receive(t, broker, 10) {
		got = append(got, delivery.Message.ID)
	}
	want := []string{messages[0].ID, messages[1].ID}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("received %v, want exactly the prefix returned %v", got, want)
	}
}

// TestFilePublishManyWithoutTheSiblingPublishesEachMessage keeps the path
// for a publisher that cannot share the flush: one publication per message.
func TestFilePublishManyWithoutTheSiblingPublishesEachMessage(t *testing.T) {
	t.Parallel()
	broker := makeFile(t, clock.NewManualClock(epoch), defaultPolicy())
	publisher := counted(t, broker, -1, false)
	if messages, err := broker.(corequeue.BatchPublisher).PublishMany(t.Context(), sixteen()[:3]); err != nil || len(messages) != 3 {
		t.Fatalf("PublishMany(3) = %d, %v, want 3, nil", len(messages), err)
	}
	if publisher.batches != 0 || publisher.single != 3 {
		t.Fatalf("PublishMany(3) made %d batched and %d single publications, want 0 and 3", publisher.batches, publisher.single)
	}
}
