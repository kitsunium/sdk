package queue_test

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"

	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
	coresql "github.com/kitsunium/sdk/internal/core/data/sql"
	svcqueue "github.com/kitsunium/sdk/internal/service/data/queue"
)

// The codes of a failure as a mail spool shapes one (issue #500): the
// transport's error is the origin, and the spool's verdict is a code it adds
// to the trail, where the dead letter's Reason, Cause and Code never show it.
const (
	trailOriginCode errs.Code = 0x00_03_35_F0
	trailMarkCode   errs.Code = 0x00_03_35_F1
)

// trailedCause is a failure whose origin is SEND_REFUSED and whose trail
// holds the MAIL_REJECTED a wrapper added, then the queue's own NOT_RETRYABLE
// mark: what a spool hands its queue's Rejecter.
func trailedCause() error {
	origin := errs.Define(trailOriginCode, "SEND_REFUSED", "the relay refused the mail", "private half")
	marked := errs.Wrap(origin, errs.WrapParams{
		Code: trailMarkCode, Reason: "MAIL_REJECTED", Public: "the mail was rejected", Private: "private half",
	})
	return corequeue.DoNotRetry(marked)
}

// wantTrail is what a dead letter keeps of trailedCause's trail.
func wantTrail() []uint32 {
	return []uint32{uint32(trailMarkCode), uint32(corequeue.CodeNotRetryable)}
}

// TestADeadLetterKeepsTheTrailOfItsCause pins issue #500 on every broker: a
// message rejected, and one nacked on its last delivery, are dead-lettered
// with the origin's reason, words and code AND the codes the wrappers added,
// oldest first; one that died of lapsed leases, and one whose cause carried no
// trail, have none; and a replayed dead letter that dies again keeps nothing
// of its first death.
func TestADeadLetterKeepsTheTrailOfItsCause(t *testing.T) {
	t.Parallel()
	for _, factory := range everyBroker() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			clk := clock.NewManualClock(epoch)
			policy := defaultPolicy()
			policy.MaxDeliveries = 1
			broker := factory.make(t, clk, policy)
			rejected := publish(t, broker, "rejected")
			if err := rejecter(t, broker).Reject(t.Context(), receiveOne(t, broker).Lease.Receipt, trailedCause()); err != nil {
				t.Fatalf("Reject() = %v", err)
			}
			nacked := publish(t, broker, "nacked")
			if verdict := nack(t, broker, receiveOne(t, broker).Lease.Receipt, trailedCause()); !verdict.DeadLettered {
				t.Fatalf("the last delivery's nack = %+v, want dead-lettered", verdict)
			}
			plain := publish(t, broker, "plain")
			nack(t, broker, receiveOne(t, broker).Lease.Receipt, errs.Define(trailOriginCode, "SEND_REFUSED", "refused", "private"))
			lapsed := publish(t, broker, "lapsed")
			receiveOne(t, broker)
			clk.Advance(testVisibility)
			receiveNone(t, broker)

			byID := map[string]corequeue.DeadLetterValue{}
			for _, dead := range deadLetters(t, broker, 10) {
				byID[dead.Message.ID] = dead
			}
			for _, id := range []string{rejected.ID, nacked.ID} {
				got := byID[id]
				if got.Reason != "SEND_REFUSED" || got.Code != uint32(trailOriginCode) || !slices.Equal(got.Trail, wantTrail()) {
					t.Errorf("dead letter %s = %q %#x trail %#x, want SEND_REFUSED %#x trail %#x",
						id, got.Reason, got.Code, got.Trail, uint32(trailOriginCode), wantTrail())
				}
			}
			for _, id := range []string{plain.ID, lapsed.ID} {
				if got, found := byID[id]; !found || got.Trail != nil {
					t.Errorf("dead letter %s (found %v) trail %#v, want nil", id, found, got.Trail)
				}
			}

			if err := manager(t, broker).ReplayDeadLetter(t.Context(), rejected.ID); err != nil {
				t.Fatalf("ReplayDeadLetter() = %v", err)
			}
			receiveOne(t, broker)
			clk.Advance(testVisibility)
			receiveNone(t, broker)
			for _, dead := range deadLetters(t, broker, 10) {
				if dead.Message.ID == rejected.ID && (dead.Reason != "LEASE_EXPIRED" || dead.Trail != nil) {
					t.Errorf("the replayed message died again as %q with trail %#x, want LEASE_EXPIRED and none", dead.Reason, dead.Trail)
				}
			}
		})
	}
}

// TestADeadLetterReadDoesNotShareItsTrail pins that the trail a reader gets
// is its own on every broker: rewriting it changes nothing the next reader
// sees.
func TestADeadLetterReadDoesNotShareItsTrail(t *testing.T) {
	t.Parallel()
	for _, factory := range everyBroker() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			broker := factory.make(t, clock.NewManualClock(epoch), defaultPolicy())
			publish(t, broker, "rejected")
			if err := rejecter(t, broker).Reject(t.Context(), receiveOne(t, broker).Lease.Receipt, trailedCause()); err != nil {
				t.Fatalf("Reject() = %v", err)
			}
			deadLetters(t, broker, 1)[0].Trail[0] = 0
			if again := deadLetters(t, broker, 1)[0].Trail; !slices.Equal(again, wantTrail()) {
				t.Errorf("a reader rewrote the stored trail: %#x", again)
			}
		})
	}
}

// TestSQLWithoutTrailSendsTheStatementsItAlwaysSent pins SQLConfig.Trail's
// zero value: a broker over a table SQLTrailMigration never touched buries,
// writes and reads its dead letters with the statements it sent before the
// column existed, and they have no trail.
func TestSQLWithoutTrailSendsTheStatementsItAlwaysSent(t *testing.T) {
	t.Parallel()
	for _, dialect := range []coresql.Dialect{coresql.DialectPostgres, coresql.DialectMySQL, coresql.DialectSQLite} {
		t.Run(dialect.String(), func(t *testing.T) {
			t.Parallel()
			fixture := newSQLFixtureTrail(t, dialect, clock.NewManualClock(epoch), defaultPolicy(), false)
			publish(t, fixture.broker, "rejected")
			if err := rejecter(t, fixture.broker).Reject(t.Context(), receiveOne(t, fixture.broker).Lease.Receipt, trailedCause()); err != nil {
				t.Fatalf("Reject() = %v", err)
			}
			given := recordedElsewhere()[2]
			if _, err := writer(t, fixture.broker).PutDeadLetter(t.Context(), given); err != nil {
				t.Fatalf("PutDeadLetter() = %v", err)
			}
			for _, dead := range deadLetters(t, fixture.broker, 10) {
				if dead.Trail != nil || dead.Code == 0 {
					t.Errorf("dead letter %+v, want its code and no trail", dead)
				}
			}
			for _, role := range fixture.engine.roles() {
				if strings.Contains(role, "trail") {
					t.Errorf("a broker without Trail sent %q", role)
				}
			}
		})
	}
}

// TestAFileDeadLetterWithoutATrailIsTheRecordItAlwaysWas pins the file
// broker's record: a cause with no trail writes no trail line — the bytes
// this format always wrote —, a record written before the line existed or
// with one that does not parse reads back with no trail and everything else
// kept, and a trail line is one line of decimal codes.
func TestAFileDeadLetterWithoutATrailIsTheRecordItAlwaysWas(t *testing.T) {
	t.Parallel()
	if diskRefused() {
		t.Skip("the file broker refuses this platform; requireFileBroker pins the refusal")
	}
	dir := t.TempDir()
	broker := requireFileBroker(t, svcqueue.FileConfig{Dir: dir, Policy: defaultPolicy(), Clock: clock.NewManualClock(epoch)})
	publish(t, broker, "plain")
	if err := rejecter(t, broker).Reject(t.Context(), receiveOne(t, broker).Lease.Receipt,
		errs.Define(trailOriginCode, "SEND_REFUSED", "refused", "private")); err != nil {
		t.Fatalf("Reject() = %v", err)
	}
	publish(t, broker, "trailed")
	if err := rejecter(t, broker).Reject(t.Context(), receiveOne(t, broker).Lease.Receipt, trailedCause()); err != nil {
		t.Fatalf("Reject() = %v", err)
	}
	plain, trailed := deadRecord(t, dir, "plain"), deadRecord(t, dir, "trailed")
	if bytes.Contains(plain.raw, []byte("\ntrail ")) {
		t.Errorf("a cause with no trail wrote a trail line:\n%s", plain.raw)
	}
	wantLine := "\ntrail " + strconv.FormatUint(uint64(trailMarkCode), 10) + "," +
		strconv.FormatUint(uint64(corequeue.CodeNotRetryable), 10) + "\n"
	if !bytes.Contains(trailed.raw, []byte(wantLine)) {
		t.Fatalf("the trailed record lacks %q:\n%s", wantLine, trailed.raw)
	}
	for name, line := range map[string]string{
		"older": "\n", "corrupt": "\ntrail 12,x\n", "too long": "\ntrail " + strings.Repeat("1,", 16) + "1\n",
	} {
		rewritten := bytes.Replace(trailed.raw, []byte(wantLine), []byte(line), 1)
		if err := os.WriteFile(trailed.path, rewritten, 0o600); err != nil {
			t.Fatal(err)
		}
		var got corequeue.DeadLetterValue
		for _, dead := range deadLetters(t, broker, 10) {
			if string(dead.Message.Payload) == "trailed" {
				got = dead
			}
		}
		if got.Trail != nil || got.Reason != "SEND_REFUSED" || got.Code != uint32(trailOriginCode) || got.Cause != "the relay refused the mail" {
			t.Errorf("the %s record read back as %+v, want its cause and no trail", name, got)
		}
	}
}

// deadFile is one file of the file broker's dead directory.
type deadFile struct {
	path string
	raw  []byte
}

// deadRecord reads the file broker's dead letter whose payload is payload.
func deadRecord(t *testing.T, dir, payload string) deadFile {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dir, "dead"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := filepath.Join(dir, "dead", entry.Name())
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.HasSuffix(raw, []byte("\n\n"+payload)) {
			return deadFile{path: name, raw: raw}
		}
	}
	t.Fatalf("no dead record carries %q", payload)
	return deadFile{}
}
