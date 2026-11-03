# datagram

Package `datagram` sends a backlog of datagrams over a connected socket in as
few system calls as the kernel allows: `sendmmsg(2)` on Linux, up to
`MaxBatch` datagrams a call, and one `Write` per datagram elsewhere (#807).
The udp network writer (`internal/service/observe/logger/writer/nettransport`)
and journald (`internal/service/observe/logger/writer/journald`) hand it the
records their async drainer holds. Every payload stays ONE datagram — nothing
is gathered — so a record is never merged with the next. It declares no error
code: each sink wraps what `Send` returns under its own. See `CLAUDE.md`.
