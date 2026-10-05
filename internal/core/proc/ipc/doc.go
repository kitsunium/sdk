// Package ipc — the private socket's codes, range 0.3.91.* (ADR 0148).
//
// The range was allocated to internal/service/proc/ipc, the engine, and is
// declared here, at the same path in the core, since ADR 0160. The values did
// not change with the move, and the LL byte still records the service layer
// that allocated them.
//
// Package ipc — the sentinels internal/service/proc/ipc refuses with. Each
// var's name equals its errs.Define Reason in SCREAMING_SNAKE form. None
// quotes a path in its Public half: the path travels in the "path" field, for
// the log. The Private strings name the service package, where each condition
// is detected.
//
// Package ipc is the private socket's contract (ADR 0148, ADR 0160): who is
// at the other end of a connection, the connection that carries that answer,
// the two ports a private socket is reached through — the Listener that
// accepts and the Dialer that connects — and the codes every refusal carries.
//
// The engine is internal/service/proc/ipc: a Unix socket in a 0700 directory
// whose whole path is audited, the kernel's word on the peer where it gives
// one, a named pipe with its own DACL on Windows. Nothing here opens a socket.
// The ports exist so a caller that holds a Listener or a Dialer can be handed
// a double in a test — connections from net.Pipe with the peer the test
// chooses — instead of a socket on disk.
package ipc
