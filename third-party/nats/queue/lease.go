package queue

import (
	"crypto/rand"
	"encoding/hex"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/kitsunium/sdk/internal/kernel/errs"

	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
)

// receiptVersion opens every receipt this package mints, so one from another
// broker type is UnknownReceipt at the first byte.
const receiptVersion string = "nj1"

// receiptSep separates a receipt's three parts: the version, the deadline in
// Unix nanoseconds, the token.
const receiptSep string = "."

// receiptBase is the base the deadline is written in.
const receiptBase int = 36

// mintBytes is the length of a broker's random identity in bytes.
const mintBytes int = 8

// leaseValue is what the broker keeps of one lease it handed out: the
// JetStream delivery that acknowledges it, the message as delivered, and the
// deadline on the broker's clock.
type leaseValue struct {
	deadline   time.Time
	msg        jetstream.Msg
	message    corequeue.MessageValue
	deliveries int
	void       bool
}

// leaseTable holds the leases one broker handed out, by token. A token is the
// broker's random identity and a counter, so a receipt minted by another
// broker never names one of these.
type leaseTable struct {
	leases   map[string]*leaseValue
	identity string
	mu       sync.Mutex
	next     uint64
}

// newLeaseTable mints the broker's identity.
func newLeaseTable() *leaseTable {
	raw := make([]byte, mintBytes)
	_, _ = rand.Read(raw)
	//: crypto/rand never fails on the platforms Go supports.
	return &leaseTable{leases: map[string]*leaseValue{}, identity: hex.EncodeToString(raw)}
}

// grant records lease and returns the receipt naming it.
func (t *leaseTable) grant(lease *leaseValue) corequeue.ReceiptValue {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.next++
	token := t.identity + strconv.FormatUint(t.next, receiptBase)
	t.leases[token] = lease
	//: the deadline travels in the receipt, so a lapsed one is known as such
	//: after the table forgot it.
	return corequeue.ReceiptValue(receiptVersion + receiptSep +
		strconv.FormatInt(lease.deadline.UnixNano(), receiptBase) + receiptSep + token)
}

// take finds the live lease receipt names at now and removes it from the
// table: LeaseExpired for a lapsed, settled or replaced lease,
// UnknownReceipt for one this broker never issued.
func (t *leaseTable) take(receipt corequeue.ReceiptValue, now time.Time) (lease *leaseValue, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	lease, token, err := t.find(receipt, now)
	//: refused, nothing removed but a lapsed lease.
	if err != nil {
		//: as find answered.
		return nil, err
	}
	delete(t.leases, token)
	//: the caller settles it.
	return lease, nil
}

// peek is take without removing the lease, for a renewal that may fail.
func (t *leaseTable) peek(receipt corequeue.ReceiptValue, now time.Time) (lease *leaseValue, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	lease, _, err = t.find(receipt, now)
	//: as find answered.
	return lease, err
}

// replace voids receipt and grants next in its place, if receipt still names
// a live lease at now.
func (t *leaseTable) replace(receipt corequeue.ReceiptValue, now time.Time, next *leaseValue) (renewed corequeue.ReceiptValue, err error) {
	t.mu.Lock()
	lease, _, ferr := t.find(receipt, now)
	//: the lease lapsed or was settled while the renewal was in flight.
	if ferr != nil {
		t.mu.Unlock()
		//: refused.
		return "", ferr
	}
	lease.void = true
	t.mu.Unlock()
	//: the old receipt is void from this moment.
	return t.grant(next), nil
}

// restore puts back a lease take removed, when the request that was to
// settle it failed and the lease still holds.
func (t *leaseTable) restore(receipt corequeue.ReceiptValue, lease *leaseValue) {
	_, _, token, ok := parseReceipt(receipt)
	//: only a receipt take accepted comes back here.
	if !ok {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.leases[token] = lease
}

// find is the lookup take and peek share; the caller holds mu.
func (t *leaseTable) find(receipt corequeue.ReceiptValue, now time.Time) (lease *leaseValue, token string, err error) {
	deadline, identity, token, ok := parseReceipt(receipt)
	//: not a receipt of this package, or not of this broker.
	if !ok || identity != t.identity {
		//: UnknownReceipt.
		return nil, "", errs.Wrap(corequeue.UnknownReceipt, errs.WrapParams{}, errs.String("broker", "jetstream"))
	}
	lease, held := t.leases[token]
	//: the deadline is the broker's clock's, and it has passed.
	if !now.Before(deadline) {
		delete(t.leases, token)
		//: LeaseExpired.
		return nil, "", leaseLost("lapsed")
	}
	//: settled, or replaced by a renewal.
	if !held || lease.void {
		//: LeaseExpired.
		return nil, "", leaseLost("settled")
	}
	//: live.
	return lease, token, nil
}

// prune forgets every lease whose deadline has passed at now: their
// receipts still read LeaseExpired from the deadline they carry.
func (t *leaseTable) prune(now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	//: one pass; the table holds what is in flight.
	for token, lease := range t.leases {
		//: lapsed.
		if !now.Before(lease.deadline) {
			delete(t.leases, token)
		}
	}
}

// parseReceipt reads a receipt grant wrote: its deadline, the identity of the
// broker that minted it, and its token.
func parseReceipt(receipt corequeue.ReceiptValue) (deadline time.Time, identity, token string, ok bool) {
	parts := strings.Split(string(receipt), receiptSep)
	//: three parts, this package's version first.
	if len(parts) != 3 || parts[0] != receiptVersion || len(parts[2]) <= 2*mintBytes {
		//: not ours.
		return time.Time{}, "", "", false
	}
	nanos, perr := strconv.ParseInt(parts[1], receiptBase, 64)
	//: a deadline that does not parse is not ours either.
	if perr != nil {
		return time.Time{}, "", "", false
	}
	//: the identity is the token's first bytes.
	return time.Unix(0, nanos), parts[2][:2*mintBytes], parts[2], true
}
