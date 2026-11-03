package queue

import (
	"strconv"
	"strings"

	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
)

// receiptVersion opens every receipt this package mints.
const receiptVersion string = "rs1"

// receiptSep separates a receipt's parts.
const receiptSep string = "|"

// receiptValue is what a receipt names: the entry, the token of its lease,
// and the delivery the lease is.
type receiptValue struct {
	id         string
	token      string
	deliveries int
}

// encode is the receipt a consumer holds.
func (r receiptValue) encode() corequeue.ReceiptValue {
	//: rs1|id|token|deliveries
	return corequeue.ReceiptValue(strings.Join([]string{receiptVersion, r.id, r.token, strconv.Itoa(r.deliveries)}, receiptSep))
}

// parseReceipt reads a receipt encode wrote.
func parseReceipt(receipt corequeue.ReceiptValue) (parsed receiptValue, ok bool) {
	parts := strings.Split(string(receipt), receiptSep)
	//: four parts, this package's version first, none empty.
	if len(parts) != 4 || parts[0] != receiptVersion || parts[1] == "" || parts[2] == "" {
		//: not ours.
		return receiptValue{}, false
	}
	deliveries, perr := strconv.Atoi(parts[3])
	//: a delivery counts from 1.
	if perr != nil || deliveries < 1 {
		return receiptValue{}, false
	}
	//: ours.
	return receiptValue{id: parts[1], token: parts[2], deliveries: deliveries}, true
}
