package machine_test

import (
	"bytes"
	"context"
	"testing"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	coresql "github.com/kitsunium/sdk/internal/core/data/sql"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/app/consensus/machine"
)

var testCluster = coreconsensus.ClusterID{7, 7, 7}

// commands returns the committed commands at indexes from..to, term 2.
func commands(from, to uint64) []coreconsensus.EntryValue {
	var es []coreconsensus.EntryValue
	for i := from; i <= to; i++ {
		es = append(es, coreconsensus.EntryValue{
			Position: coreconsensus.PositionValue{Term: 2, Index: i},
			Kind:     coreconsensus.EntryCommand,
			Data:     []byte("c" + string(rune('a'+i))),
			Token:    coreconsensus.TokenValue{Cluster: testCluster, Epoch: 1, Term: 2, Index: i},
		})
	}
	return es
}

func mustSQL(t *testing.T, cfg machine.SQLConfig) *machine.SQL {
	t.Helper()
	m, err := machine.NewSQL(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewSQL() = %v", err)
	}
	return m
}

func TestNewSQLRefusesAConfigurationItCannotRun(t *testing.T) {
	_, tm := newDB(t)
	type tc struct {
		name string
		edit func(c *machine.SQLConfig)
	}
	cases := []tc{
		{"no transactor", func(c *machine.SQLConfig) { c.Transactor = nil }},
		{"no dialect", func(c *machine.SQLConfig) { c.Dialect = coresql.DialectUnknown }},
		{"no apply", func(c *machine.SQLConfig) { c.Apply = nil }},
		{"no load", func(c *machine.SQLConfig) { c.Load = nil }},
		{"a table that needs quoting", func(c *machine.SQLConfig) { c.Table = `x"; DROP TABLE y` }},
		{"a table past 63 bytes", func(c *machine.SQLConfig) { c.Table = string(bytes.Repeat([]byte("a"), 64)) }},
	}
	runCase := func(t *testing.T, c tc) {
		cfg := entriesConfig(tm)
		c.edit(&cfg)
		_, err := machine.NewSQL(context.Background(), cfg)
		if !errs.HasCode(err, coreconsensus.CodeConsensusMisconfigured) {
			t.Fatalf("NewSQL() = %v, want CONSENSUS_MISCONFIGURED", err)
		}
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

func TestABatchAndItsPositionCommitTogether(t *testing.T) {
	d, tm := newDB(t)
	m := mustSQL(t, entriesConfig(tm))
	if _, pos, _ := m.AppliedPosition(); pos != (coreconsensus.PositionValue{}) {
		t.Fatalf("a new database holds position %+v", pos)
	}
	es := commands(1, 3)
	results := make([]any, len(es))
	m.ApplyBatch(es, results)
	for i, r := range results {
		if r != string(es[i].Data) {
			t.Errorf("results[%d] = %v, want %q", i, r, es[i].Data)
		}
	}
	if got := len(d.rows()); got != 3 {
		t.Fatalf("%d rows committed, want 3", got)
	}
	reopened := mustSQL(t, entriesConfig(transactorOver(t, d)))
	li, pos, _ := reopened.AppliedPosition()
	if pos != es[2].Position || li.Cluster != testCluster || li.Epoch != 1 {
		t.Fatalf("a reopened machine holds %+v %+v, want %+v of cluster %v epoch 1", li, pos, es[2].Position, testCluster)
	}
}

func TestABatchWhosePositionWriteFailsLeavesNothing(t *testing.T) {
	d, tm := newDB(t)
	m := mustSQL(t, entriesConfig(tm))
	m.ApplyBatch(commands(1, 2), make([]any, 2))
	d.armCrash(3)
	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Fatal("a batch whose position write failed did not panic")
			} else if err, _ := r.(error); !errs.HasCode(err, coreconsensus.CodeStateMachineFailed) {
				t.Fatalf("panic(%v), want STATE_MACHINE_FAILED", r)
			}
		}()
		m.ApplyBatch(commands(3, 5), make([]any, 3))
	}()
	if got := len(d.rows()); got != 2 {
		t.Fatalf("%d rows committed after the crash, want the 2 of the batch before", got)
	}
	_, pos, _ := mustSQL(t, entriesConfig(transactorOver(t, d))).AppliedPosition()
	if pos.Index != 2 {
		t.Fatalf("the reopened machine resumes after %d, want 2", pos.Index)
	}
}

func TestASnapshotIsTheStateWhenItWasTaken(t *testing.T) {
	_, tm := newDB(t)
	m := mustSQL(t, entriesConfig(tm))
	m.ApplyBatch(commands(1, 2), make([]any, 2))
	snap, err := m.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot() = %v", err)
	}
	m.ApplyBatch(commands(3, 4), make([]any, 2))
	var buf bytes.Buffer
	n, err := snap.WriteTo(&buf)
	snap.Release()
	snap.Release()
	if err != nil || n != int64(buf.Len()) {
		t.Fatalf("WriteTo() = %d, %v for %d bytes", n, err, buf.Len())
	}
	d2, tm2 := newDB(t)
	m2 := mustSQL(t, entriesConfig(tm2))
	meta := coreconsensus.SnapshotMetaValue{
		Position: coreconsensus.PositionValue{Term: 2, Index: 2},
		Log:      coreconsensus.LogIdentityValue{Cluster: testCluster, Epoch: 1, KeptIndex: 9},
	}
	m2.ApplyBatch(commands(1, 1), make([]any, 1)) // replaced by the restore
	if err := m2.Restore(&buf, meta); err != nil {
		t.Fatalf("Restore() = %v", err)
	}
	if rows := d2.rows(); len(rows) != 2 || rows[1].idx != 2 {
		t.Fatalf("restored %+v, want the two rows the snapshot was taken over", rows)
	}
	li, pos, _ := mustSQL(t, entriesConfig(transactorOver(t, d2))).AppliedPosition()
	if pos != meta.Position || li != meta.Log {
		t.Fatalf("the restored position is %+v %+v, want the snapshot's %+v %+v", li, pos, meta.Log, meta.Position)
	}
}

func TestARestoreRefusesASnapshotItCannotRead(t *testing.T) {
	_, tm := newDB(t)
	m := mustSQL(t, entriesConfig(tm))
	type tc struct {
		name string
		body string
		code errs.Code
	}
	cases := []tc{
		{"another machine's", "KKV\x01", coreconsensus.CodeSnapshotCorrupt},
		{"cut short", "KS", coreconsensus.CodeSnapshotCorrupt},
		{"a newer format", "KSQL\x02", coreconsensus.CodeVersionUnsupported},
	}
	runCase := func(t *testing.T, c tc) {
		err := m.Restore(bytes.NewReader([]byte(c.body)), coreconsensus.SnapshotMetaValue{})
		if !errs.HasCode(err, c.code) {
			t.Fatalf("Restore() = %v, want %v", err, c.code)
		}
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}
