// Package spool — range 0.3.81.* (ADR 0111 service/app/mail/spool block,
// declared here since ADR 0160).
//
// Package spool — declares the sentinel *errs.Error outcomes of the durable
// mail outbox. Each var's name equals its errs.Define Reason in SCREAMING_SNAKE
// form.
package spool
