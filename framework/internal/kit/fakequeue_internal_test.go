package kit

import (
	"bytes"
	"database/sql/driver"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// The fake database's queue tables: the statements the SDK's SQL broker
// sends (ADR 0151) on one of kit's consumer queues (ADR 0178 C1), on the
// transaction's copy like every other statement — what a rollback drops,
// the messages a publication wrote in it included.

// fakeMessage is one row of a queue table: NULL where lease, reason, cause
// or code are nil.
type fakeMessage struct {
	id                              string
	dead, due, enqueued, deliveries int64
	lease, payload, reason, cause   []byte
	code                            driver.Value
}

// Messages are the payloads table holds, live and dead, oldest first.
func (f *FakeDB) Messages(table string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	rows := slices.SortedFunc(maps.Values(f.data.queues[table]), func(a, b fakeMessage) int { return strings.Compare(a.id, b.id) })
	out := make([]string, 0, len(rows))
	for _, m := range rows {
		out = append(out, string(m.payload))
	}
	return out
}

// queueStatement runs one of the SQL broker's statements on its table.
func queueStatement(d *fakeData, query, table string, args []driver.NamedValue) (driver.Rows, int64, error) {
	switch {
	case strings.HasPrefix(query, "CREATE TABLE IF NOT EXISTS "):
		if d.queues[table] == nil {
			d.queues[table] = map[string]fakeMessage{}
		}
		return nil, 0, nil
	case strings.HasPrefix(query, "DROP TABLE IF EXISTS "):
		delete(d.queues, table)
		return nil, 0, nil
	}
	q := d.queues[table]
	if q == nil {
		return nil, 0, fmt.Errorf("fake: no queue table %s", table)
	}
	a := fakeArgs(args)
	switch {
	case strings.HasPrefix(query, "INSERT INTO ") && strings.Contains(query, "reason, cause, code"):
		id := string(a.bytes(0))
		q[id] = fakeMessage{
			id: id, dead: 1, due: a.int(1), enqueued: a.int(2), deliveries: a.int(3), payload: a.bytes(4),
			reason: a.bytes(5), cause: a.bytes(6), code: a.int(7),
		}
		return nil, 1, nil
	case strings.HasPrefix(query, "INSERT INTO ") && len(a) == 5:
		id := string(a.bytes(0))
		q[id] = fakeMessage{id: id, due: a.int(1), enqueued: a.int(2), deliveries: a.int(3), payload: a.bytes(4)}
		return nil, 1, nil
	case strings.HasPrefix(query, "INSERT INTO "):
		id := string(a.bytes(0))
		q[id] = fakeMessage{id: id, due: a.int(1), enqueued: a.int(2), payload: a.bytes(3)}
		return nil, 1, nil
	case strings.HasPrefix(query, "DELETE FROM ") && strings.HasSuffix(query, "WHERE 1 = 0"):
		return nil, 0, nil
	case strings.HasPrefix(query, "SELECT MIN(due)"):
		return fakeQMinDue(q, strings.Contains(query, "due >"), a), 0, nil
	case strings.HasPrefix(query, "SELECT id, enqueued_at, deliveries, lease, payload"):
		return fakeQPickDue(q, a.int(0), a.int(1)), 0, nil
	case strings.HasPrefix(query, "SELECT id, enqueued_at, deliveries, due, lease, payload"):
		return fakeQLive(q, string(a.bytes(0)), a.int(1)), 0, nil
	case strings.HasPrefix(query, "SELECT id, enqueued_at, deliveries, due"):
		return fakeQDeadLetters(q, a.int(0)), 0, nil
	}
	return queueWrite(q, query, a)
}

// queueWrite runs one of the broker's writes: by the message a receipt
// holds, by a batch of ids, or by a dead letter's id.
func queueWrite(q map[string]fakeMessage, query string, a fakeArgs) (driver.Rows, int64, error) {
	switch {
	case strings.Contains(query, "deliveries = deliveries + 1"):
		return nil, fakeQBatch(q, a, 2, func(m *fakeMessage) { m.due, m.lease, m.deliveries = a.int(0), a.bytes(1), m.deliveries+1 }), nil
	case strings.HasPrefix(query, "UPDATE ") && strings.Contains(query, "dead = 1") && strings.Contains(query, "id IN ("):
		return nil, fakeQBatch(q, a, 4, func(m *fakeMessage) { fakeQBury(m, a) }), nil
	case strings.HasPrefix(query, "DELETE FROM ") && strings.Contains(query, "AND lease ="):
		return nil, fakeQHeld(q, a, 0, func(m *fakeMessage) bool { return false }), nil
	case strings.HasPrefix(query, "UPDATE ") && strings.Contains(query, "SET due = ") && strings.Contains(query, "lease = NULL WHERE id"):
		return nil, fakeQHeld(q, a, 1, func(m *fakeMessage) bool { m.due, m.lease = a.int(0), nil; return true }), nil
	case strings.HasPrefix(query, "UPDATE ") && strings.Contains(query, "SET dead = 1"):
		return nil, fakeQHeld(q, a, 4, func(m *fakeMessage) bool { fakeQBury(m, a); return true }), nil
	case strings.HasPrefix(query, "UPDATE ") && strings.Contains(query, "SET due = ") && strings.Contains(query, ", lease = "):
		return nil, fakeQHeld(q, a, 2, func(m *fakeMessage) bool { m.due, m.lease = a.int(0), a.bytes(1); return true }), nil
	case strings.HasPrefix(query, "UPDATE ") && strings.Contains(query, "SET dead = 0"):
		return nil, fakeQDeadOne(q, string(a.bytes(1)), func(m *fakeMessage) bool {
			m.dead, m.due, m.deliveries, m.reason, m.cause, m.code = 0, a.int(0), 0, nil, nil, nil
			return true
		}), nil
	case strings.HasPrefix(query, "DELETE FROM "):
		return nil, fakeQDeadOne(q, string(a.bytes(0)), func(*fakeMessage) bool { return false }), nil
	}
	return nil, 0, fmt.Errorf("fake: a queue statement it does not know: %q", query)
}

// fakeQBury marks m dead with the cause a binds first.
func fakeQBury(m *fakeMessage, a fakeArgs) {
	m.dead, m.due, m.lease, m.reason, m.cause, m.code = 1, a.int(0), nil, a.bytes(1), a.bytes(2), a.int(3)
}

// fakeQMinDue is MIN(due) over the live messages, those due after the bound when
// after is set; NULL when there is none.
func fakeQMinDue(q map[string]fakeMessage, after bool, a fakeArgs) driver.Rows {
	var least driver.Value
	for _, m := range q {
		if m.dead != 0 || (after && m.due <= a.int(0)) {
			continue
		}
		if least == nil || m.due < least.(int64) {
			least = m.due
		}
	}
	return &fakeRows{cols: []string{"min"}, rows: [][]driver.Value{{least}}}
}

// fakeQSortedMessages are q's messages for which keep holds, by due then id.
func fakeQSortedMessages(q map[string]fakeMessage, keep func(m fakeMessage) bool) []fakeMessage {
	var out []fakeMessage
	for _, m := range q {
		if keep(m) {
			out = append(out, m)
		}
	}
	slices.SortFunc(out, func(x, y fakeMessage) int {
		if x.due != y.due {
			return int(x.due - y.due)
		}
		return strings.Compare(x.id, y.id)
	})
	return out
}

// fakeQPickDue are the live messages due by now, at most limit of them.
func fakeQPickDue(q map[string]fakeMessage, now, limit int64) driver.Rows {
	out := &fakeRows{cols: []string{"id", "enqueued_at", "deliveries", "lease", "payload"}}
	for _, m := range fakeQSortedMessages(q, func(m fakeMessage) bool { return m.dead == 0 && m.due <= now }) {
		if int64(len(out.rows)) == limit {
			break
		}
		out.rows = append(out.rows, []driver.Value{[]byte(m.id), m.enqueued, m.deliveries, nilOr(m.lease), m.payload})
	}
	return out
}

// fakeQDeadLetters are the dead messages, at most limit of them.
func fakeQDeadLetters(q map[string]fakeMessage, limit int64) driver.Rows {
	out := &fakeRows{cols: []string{"id", "enqueued_at", "deliveries", "due", "reason", "cause", "code", "payload"}}
	for _, m := range fakeQSortedMessages(q, func(m fakeMessage) bool { return m.dead == 1 }) {
		if int64(len(out.rows)) == limit {
			break
		}
		out.rows = append(out.rows, []driver.Value{[]byte(m.id), m.enqueued, m.deliveries, m.due, nilOr(m.reason), nilOr(m.cause), m.code, m.payload})
	}
	return out
}

// fakeQLive is the live messages whose id sorts after after, by id, at most
// limit: a copy's listing.
func fakeQLive(q map[string]fakeMessage, after string, limit int64) driver.Rows {
	out := &fakeRows{cols: []string{"id", "enqueued_at", "deliveries", "due", "lease", "payload"}}
	live := slices.SortedFunc(maps.Values(q), func(a, b fakeMessage) int { return strings.Compare(a.id, b.id) })
	for _, m := range live {
		if m.dead != 0 || m.id <= after {
			continue
		}
		if int64(len(out.rows)) == limit {
			break
		}
		out.rows = append(out.rows, []driver.Value{[]byte(m.id), m.enqueued, m.deliveries, m.due, nilOr(m.lease), m.payload})
	}
	return out
}

// fakeQBatch applies fn to every live message whose id is bound from first on,
// and counts them.
func fakeQBatch(q map[string]fakeMessage, a fakeArgs, first int, fn func(m *fakeMessage)) int64 {
	var n int64
	for i := first; i < len(a); i++ {
		m, ok := q[string(a.bytes(i))]
		if !ok || m.dead != 0 {
			continue
		}
		fn(&m)
		q[m.id] = m
		n++
	}
	return n
}

// fakeQHeld applies fn to the live message the receipt bound from first on holds
// — its id, its lease, its deliveries, a lease not lapsed —, deleting it
// when fn reports false, and counts it.
func fakeQHeld(q map[string]fakeMessage, a fakeArgs, first int, fn func(m *fakeMessage) bool) int64 {
	m, ok := q[string(a.bytes(first))]
	if !ok || m.dead != 0 || !bytes.Equal(m.lease, a.bytes(first+1)) || m.deliveries != a.int(first+2) || m.due <= a.int(first+3) {
		return 0
	}
	if !fn(&m) {
		delete(q, m.id)
		return 1
	}
	q[m.id] = m
	return 1
}

// fakeQDeadOne applies fn to the dead message id, deleting it when fn reports
// false, and counts it.
func fakeQDeadOne(q map[string]fakeMessage, id string, fn func(m *fakeMessage) bool) int64 {
	m, ok := q[id]
	if !ok || m.dead != 1 {
		return 0
	}
	if !fn(&m) {
		delete(q, id)
		return 1
	}
	q[id] = m
	return 1
}

// fakeArgs are a statement's bound values.
type fakeArgs []driver.NamedValue

// bytes is the i-th value as bytes.
func (a fakeArgs) bytes(i int) []byte { return bound(a[i].Value) }

// int is the i-th value as an integer.
func (a fakeArgs) int(i int) int64 {
	n, _ := a[i].Value.(int64)
	return n
}
