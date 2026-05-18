// Package syslog implements a network sink that ships records to a syslog
// daemon via UDP or TCP. The wire format is a minimal RFC5424 envelope:
//
//	<PRI>1 - - - - - - <payload>
//
// PRI is computed from the record's level + the default USER facility.
// Hostname, app-name, procid, msgid, and structured-data slots are fixed
// to "-" to keep the producer side allocation-friendly; consumers that
// need richer envelopes wrap this sink with their own framing.
//
// Use case: ship logs to journald / rsyslog / a central syslog collector
// over the standard 514/UDP port.
package syslog

import (
	"context"
	"net"
	"strconv"
	"sync"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// netUDP is the canonical udp network identifier accepted by net.Dial.
const netUDP string = "udp"

// netTCP is the canonical tcp network identifier accepted by net.Dial.
const netTCP string = "tcp"

// envelopeSuffix is the "1 - - - - - - " RFC5424 envelope suffix written
// after the <PRI> token. The trailing space separates header from message.
const envelopeSuffix string = "1 - - - - - - "

// frameHeaderHint is the byte budget reserved for the "<PRI>1 - - - - - - "
// header so a typical frame fits in a single allocation alongside the
// payload. PRI is at most 3 digits + 2 brackets + envelopeSuffix's length.
const frameHeaderHint int = 16

// decimalBase is the base passed to strconv.AppendInt when rendering the
// PRI integer into the frame buffer.
const decimalBase int = 10

// syslogSink wraps a net.Conn behind a mutex so concurrent producers emit
// atomic frames (UDP datagrams are inherently atomic; the mutex protects
// the TCP framing path).
type syslogSink struct {
	// conn is the underlying network connection.
	conn net.Conn
	// mu serialises Write calls so concurrent producers emit atomic frames.
	mu sync.Mutex
}

// New dials addr over network (udp / tcp) using net.Dial and returns a
// syslog Sink. The connection lifetime is owned by the sink — Close
// releases it. SECURITY: addr is passed straight to net.Dial. When addr
// is consumer-controlled (env var, config, API input), prefer
// NewWithConfig with a Dialer that enforces an allowlist so an attacker
// cannot target internal services (169.254.169.254, localhost, etc.).
func New(network, addr string) (sink corelogger.Sink, err error) {
	//: thin wrapper around NewWithConfig with the default net.Dial dialer.
	return NewWithConfig(network, addr, Config{})
}

// NewWithConfig dials addr over network (udp / tcp) using cfg.Dialer (or
// net.Dial when cfg.Dialer is nil) and returns a syslog Sink. Recommended
// constructor when addr might be consumer-controlled — plug an allowlist
// dialer to defuse SSRF.
func NewWithConfig(network, addr string, cfg Config) (sink corelogger.Sink, err error) {
	//: refuse an empty address early — net.Dial would surface a confusing error.
	if addr == "" {
		//: documented sentinel — caller must supply an address.
		return nil, AddrEmpty
	}
	//: refuse unsupported networks — only udp / tcp are wire-tested today.
	if network != netUDP && network != netTCP {
		//: documented sentinel — caller must supply udp or tcp.
		return nil, ProtoInvalid
	}
	//: nil dialer falls back to stdlib net.Dial (legacy behaviour of New).
	dial := cfg.Dialer
	//: fallback is outside the hot path — single branch keeps call costs flat.
	if dial == nil {
		//: default dialer preserves zero-config usage.
		dial = net.Dial
	}
	//: dial the syslog target; failure is wrapped for HasCode introspection.
	conn, derr := dial(network, addr)
	//: surface dial errors via errs.Wrap so errors.Is still catches the cause.
	if derr != nil {
		//: wrap with the documented sentinel for HasCode introspection.
		return nil, errs.Wrap(derr, errs.WrapParams{
			Code:    CodeSyslogDialFailed,
			Reason:  "SYSLOG_DIAL_FAILED",
			Public:  "Syslog sink could not dial the destination",
			Private: "service/logger/sink/syslog.New: net.Dial returned an error",
		}, errs.String("network", network), errs.String("addr", addr))
	}
	//: defensive close-on-error defer satisfies the lifecycle linter; the
	//: sink owns the connection on the success path through Close.
	defer func() {
		//: skip the close on the success path — sink owns the connection.
		if err == nil {
			//: nothing to do; the constructor returned the sink to the caller.
			return
		}
		//: best-effort close; err already carries the meaningful failure cause.
		swallowDialClose(conn.Close())
	}()
	//: hand back the sink behind the public Sink interface.
	return &syslogSink{conn: conn}, nil
}

// swallowDialClose drops a Close error from the constructor's defer path.
// The original construction error (if any) carries the meaningful failure;
// stacking the close error on top would obscure it.
func swallowDialClose(err error) {
	//: read the parameter so the unused-param audit treats this no-op as intentional.
	if err == nil {
		//: nothing to discard on the happy path.
		return
	}
}

// Write frames p as RFC5424 and ships it over the network connection.
func (s *syslogSink) Write(ctx context.Context, rec corelogger.RecordEvent, p []byte) (n int, err error) {
	//: honour cancellation so a doomed request does not waste a packet.
	if ctx != nil && ctx.Err() != nil {
		//: wrap ctx.Err() so the typed-errors-only SDK rule is preserved
		//: and consumers can HasCode / errors.Is against the cancellation.
		return 0, errs.Wrap(ctx.Err(), errs.WrapParams{
			Code:    CodeSyslogCtxCancelled,
			Reason:  "SYSLOG_CTX_CANCELLED",
			Public:  "Syslog sink write aborted due to cancellation",
			Private: "service/logger/sink/syslog.Write saw a cancelled context",
		}, errs.Int("level", int(rec.Level)))
	}
	//: build the RFC5424 frame: <PRI>1 - - - - - - <payload>.
	frame := makeFrame(priorityFor(rec.Level), p)
	//: serialise writes so concurrent producers emit atomic frames.
	s.mu.Lock()
	written, werr := s.conn.Write(frame)
	s.mu.Unlock()
	//: wrap any net.Conn error with the WriteFailed sentinel semantics.
	if werr != nil {
		//: propagate through errs.Wrap so errors.Is still catches the cause.
		return written, errs.Wrap(werr, errs.WrapParams{
			Code:    CodeSyslogWriteFailed,
			Reason:  "SYSLOG_WRITE_FAILED",
			Public:  "Syslog write failed",
			Private: "service/logger/sink/syslog.Write underlying net.Conn returned an error",
		}, errs.Int("bytes", len(frame)), errs.Int("level", int(rec.Level)))
	}
	//: happy path — return the byte count.
	return written, nil
}

// Flush is a no-op for the syslog sink (UDP datagrams settle on send;
// TCP delegates to the kernel buffer).
func (s *syslogSink) Flush(ctx context.Context) error {
	//: honour cancellation even though there is nothing buffered to flush.
	if ctx != nil && ctx.Err() != nil {
		//: wrap ctx.Err() so the typed-errors-only SDK rule is preserved.
		return errs.Wrap(ctx.Err(), errs.WrapParams{
			Code:    CodeSyslogCtxCancelled,
			Reason:  "SYSLOG_CTX_CANCELLED",
			Public:  "Syslog sink flush aborted due to cancellation",
			Private: "service/logger/sink/syslog.Flush saw a cancelled context",
		})
	}
	//: net.Conn writes settle synchronously — nothing buffered on our side.
	return nil
}

// Close releases the underlying network connection. Subsequent Writes will
// fail with the kernel's "use of closed network connection" error wrapped
// as WriteFailed.
func (s *syslogSink) Close() error {
	//: serialise the close against in-flight writes via the same mutex.
	s.mu.Lock()
	cerr := s.conn.Close()
	s.mu.Unlock()
	//: wrap any close error with the CloseFailed sentinel semantics.
	if cerr != nil {
		//: propagate through errs.Wrap so errors.Is still catches the cause.
		return errs.Wrap(cerr, errs.WrapParams{
			Code:    CodeSyslogCloseFailed,
			Reason:  "SYSLOG_CLOSE_FAILED",
			Public:  "Syslog close failed",
			Private: "service/logger/sink/syslog.Close underlying net.Conn.Close returned an error",
		})
	}
	//: happy path — nothing to report.
	return nil
}

// makeFrame builds the RFC5424 envelope around p.
func makeFrame(pri int, p []byte) []byte {
	//: build the frame in a single allocation sized for header + payload.
	frame := make([]byte, 0, len(p)+frameHeaderHint)
	//: prefix with the RFC5424 PRI token "<N>".
	frame = append(frame, '<')
	frame = strconv.AppendInt(frame, int64(pri), decimalBase)
	frame = append(frame, '>')
	//: minimal envelope: VERSION + 6 NIL fields.
	frame = append(frame, envelopeSuffix...)
	//: append the payload verbatim — the encoder owns the message format.
	return append(frame, p...)
}
