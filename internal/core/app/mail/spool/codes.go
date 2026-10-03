// Package spool — range 0.3.81.* (ADR 0111 service/app/mail/spool block,
// declared here since ADR 0160).
package spool

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.81.0 - 0.3.81.255
//
// The range was allocated to internal/service/app/mail/spool, which raises
// these codes, and it is declared here, in the core mirror of that package's
// path, so that a domain's codes are never in the service layer (ADR 0160). A
// code keeps the value its allocation gave it whichever layer declares it, so
// the layer byte still reads 3.

// CodeSpoolMisconfigured identifies a spool refused at construction — no
// transport, no attempt budget, a default sender that is not an address — or
// an identifier Config.NewID got wrong, refused at Send.
const CodeSpoolMisconfigured errs.Code = 0x00_03_51_01 // 0.3.81.1

// CodeSpoolClosed identifies a Send or a SendWithID on a spool that has been
// closed.
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

// CodeInvalidMailID identifies an identifier SendWithID was given that no
// mail can keep: empty, longer than MaxIDBytes, or not an RFC 5322 dot-atom.
const CodeInvalidMailID errs.Code = 0x00_03_51_06 // 0.3.81.6
