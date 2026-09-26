// Package spool — range 0.3.81.* (ADR 0111 service/mail/spool block).
package spool

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.81.0 - 0.3.81.255

// CodeSpoolMisconfigured identifies a spool refused at construction: no
// transport, no attempt budget, a default sender that is not an address.
const CodeSpoolMisconfigured errs.Code = 0x00_03_51_01 // 0.3.81.1

// CodeSpoolClosed identifies a Send on a spool that has been closed.
const CodeSpoolClosed errs.Code = 0x00_03_51_02 // 0.3.81.2

// CodeMessageUndecodable identifies a spooled record that does not decode —
// written by another program, or edited — and is dead-lettered like any
// failed delivery.
const CodeMessageUndecodable errs.Code = 0x00_03_51_03 // 0.3.81.3

// CodeMessageUnencodable identifies a mail Send could not write into the
// spool — encoding/json refuses a Date outside the years 0 to 9999.
const CodeMessageUnencodable errs.Code = 0x00_03_51_04 // 0.3.81.4

// CodeTransportPanicked identifies a delivery attempt whose transport
// panicked: recovered, it is that attempt's failure like any other.
const CodeTransportPanicked errs.Code = 0x00_03_51_05 // 0.3.81.5
