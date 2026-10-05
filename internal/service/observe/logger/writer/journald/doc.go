// Package journald — the config Decoder making "journald" reachable from a
// config file via pkg/v1/observe/logger.FromConfig. Only plain-data keys are decodable
// (socket_path / min_level / buffer_size); the Dialer seam is code-only. A
// malformed shape yields the shared core/observe/logger/writer.WriterConfigInvalid (no
// per-package code), tagged with the writer name only — never the value.
//
// Package journald registers the "journald" writer factory (ADR 0015): a
// stdlib unix-datagram sink that ships records to the systemd journal. Importing
// the package self-registers the factory (no init()), so
// writer.Open("journald", journald.Config{…}) and YAML FromConfig
// topologies resolve. Linux-only in practice (the socket is systemd's), but the
// code is plain stdlib net and builds everywhere; on a host without journald the
// Open simply fails with JournaldOpenFailed. On Windows, whose AF_UNIX sockets
// are stream-only, the default dialer cannot connect a datagram socket at all
// and Open refuses it with UNSUPPORTED_PLATFORM before trying (ADR 0018).
//
// Package journald — the Config value type, in its own file per the
// one-exported-struct-per-file convention.
//
// Package journald — journaldSink, the terminal datagram sink. It frames each
// record as a single "MESSAGE=<line>\n" journal entry into a reused buffer and
// sends it as one unix datagram, so the steady-state Write path allocates
// nothing (the buffer grows once, then is reused under the mutex). Non-blocking
// back-pressure is provided by the async middleware composed around it.
//
// Framing note: the native journald protocol length-prefixes multiline field
// values; this sink emits the simple "MESSAGE=<line>\n" form, which is correct
// because the SDK encoders strip CR/LF/NUL from the message — a record is always
// a single line at this layer.
//
// Package journald — the journal socket's family, left to the kernel here.
//
// Package journald — the journal socket's family, which Windows does not have.
//
// Package journald — the wrap points every failure of this writer goes through,
// so each carries its code from internal/core/observe/logger/writer/journald.
package journald
