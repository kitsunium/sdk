package consensus

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// A learner admitted with a MaxSnapshotBytes below the state's size stopped
// every cut of every member for as long as it stayed: the log was never
// compacted again, and grew to the NO_SPACE alarm (#591). A learner's limit
// no longer bounds the cuts; Health still reports it, and the learner is
// not made a voter while the state does not fit in it.
func TestALearnerAnnouncingALimitBelowTheStateDoesNotStopCuts(t *testing.T) {
	c := newCluster(t, 3, 20)
	l := c.settledLeader(5 * time.Second)
	for i := range 40 {
		propose(t, l.node, fmt.Sprintf("key-%03d=value-%03d", i, i))
	}
	time.Sleep(300 * time.Millisecond)
	firstBefore, _ := l.log.Bounds()
	learner := coreconsensus.MemberValue{
		ID: 999, Slot: "slot-small", SPKI: MemorySPKI(999), MaxSnapshotBytes: 64,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := l.node.AddLearner(ctx, "operator:test", learner); err != nil {
		t.Fatalf("AddLearner with a 64-byte limit was refused: %v", err)
	}
	for i := range 200 {
		propose(t, l.node, fmt.Sprintf("more-%03d=value-%03d", i, i))
	}
	time.Sleep(500 * time.Millisecond)
	first, last := l.log.Bounds()
	h := l.node.Health()
	if first <= firstBefore+20 {
		t.Errorf("the log was not compacted over 200 entries after a learner announcing 64 bytes was admitted: first %d -> %d, last %d", firstBefore, first, last)
	}
	if h.SnapshotLimit != 64 || !h.Flags.Has(HealthSnapshotNearLimit) {
		t.Errorf("Health reports a limit of %d, near it %v: want the learner's 64, passed", h.SnapshotLimit, h.Flags.Has(HealthSnapshotNearLimit))
	}
	err := l.node.Promote(ctx, "operator:test", learner.ID)
	if !errors.Is(err, coreconsensus.MembershipInvalid) || !hasField(err, "limit") {
		t.Errorf("promoting a learner the state does not fit in was %v, want MEMBERSHIP_INVALID naming its limit", err)
	}
}

// hasField reports whether err carries a field under key.
func hasField(err error, key string) bool {
	for _, f := range errs.FieldsOf(err) {
		if f.Key() == key {
			return true
		}
	}
	return false
}

// A voter's announced limit still bounds the cuts (#512): only a learner's
// is left out of the bound.
func TestACutIsBoundByTheVotersLimitsOnly(t *testing.T) {
	conf := coreconsensus.MembershipValue{Members: []coreconsensus.MemberValue{
		{ID: 1, Voter: true, MaxSnapshotBytes: 4096},
		{ID: 2, Voter: true},
		{ID: 3, MaxSnapshotBytes: 64},
	}}
	if got := cutLimit(1<<20, &conf); got != 4096 {
		t.Errorf("cutLimit %d, want the voter's 4096", got)
	}
	if got := snapshotLimit(1<<20, &conf); got != 64 {
		t.Errorf("snapshotLimit %d, want the learner's 64", got)
	}
	meta := coreconsensus.SnapshotMetaValue{EncodedSize: 100, DecodedSize: 90}
	if _, over := tooLargeFor(&conf, 3, &meta); !over {
		t.Error("a 100-byte snapshot is offered to a learner announcing 64")
	}
	if _, over := tooLargeFor(&conf, 2, &meta); over {
		t.Error("a snapshot is withheld from a voter that announced no limit")
	}
}
