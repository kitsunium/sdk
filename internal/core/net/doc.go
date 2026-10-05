// Package net — the listen address value.
//
// Package net — the completed-call observation record.
//
// Package net — the outbound client configuration.
//
// Package net — range 0.2.11.* (ADR 0029 core/net block).
//
// Package net — the accepted stream connection port.
//
// Package net — the shutdown signal a long-lived handler observes.
//
// Package net — the configuration-friendly duration value.
//
// Package net — declares the sentinel *errs.Error outcomes of the network
// domain. Each var's name equals its errs.Define Reason in SCREAMING_SNAKE form.
// Service implementations and the pkg/v1 facades wrap these sentinels; they
// declare no codes of their own (the ADR 0016 proc precedent).
//
// Package net — compile-time interface assertions, kept out of the production
// source per KTN-IFACE-ASSERT-PLACEMENT.
//
// Package net — the opaque, redacting TLS identity value.
//
// Package net — the on-disk TLS material description.
//
// Package net — the TLS identity construction parameters.
//
// Package net — the per-group resource ceilings.
//
// Package net — TLS material parsing helpers shared by NewIdentity.
//
// Package net — the generic handler decorator.
//
// Package net — the received datagram port.
//
// Package net — the server lifecycle phase.
//
// Package net — the outbound authorisation port.
//
// Package net — compile-time interface assertions, kept out of the production
// source per KTN-IFACE-ASSERT-PLACEMENT.
//
// Package net — the completed outbound response value.
//
// Package net — the Server-Sent Events frame: the value a stream sends and the
// rules a frame must satisfy to be carried at all. Writing it — the encoder
// that splits Data into data lines, and the comment frame — is
// internal/service/net/sse's (ADR 0160 §4).
//
// Package net — the server's reported state.
//
// Package net — the per-phase deadlines.
//
// Package net — the WebSocket protocol's vocabulary (RFC 6455): the opening
// handshake's constants and the control-frame ceiling. Reading and writing the
// protocol — the handshake's key check and digest, the frame codec, the close
// payload, the UTF-8 check — is internal/service/net/websocket's (ADR 0160 §4).
//
// Package net — the WebSocket close code (RFC 6455 §7.4) and the predicates
// that govern it in both directions. The close payload's wire form is
// internal/service/net/websocket's (ADR 0160 §4).
//
// Package net — the WebSocket application message (RFC 6455 §5.6). Its UTF-8
// rule (§8.1) is checked by the engine on the bytes, as they come off and go
// onto the wire: internal/service/net/websocket.ValidateText (ADR 0160 §4).
//
// Package net — the WebSocket frame opcode (RFC 6455 §5.2).
//
// Package net — shared sentinel-wrapping helper.
package net
