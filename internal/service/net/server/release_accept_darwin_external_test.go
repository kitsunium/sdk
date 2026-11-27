//go:build darwin

package server_test

import "syscall"

// tcpConnectionInfo is TCP_CONNECTION_INFO of XNU's bsd/netinet/tcp.h, which
// the standard library's syscall package does not name.
const tcpConnectionInfo = 0x106

// tcpStateListen is TCPS_LISTEN of XNU's bsd/netinet/tcp_fsm.h.
const tcpStateListen = 1

// acceptsConnections reports whether the TCP socket fd is listening. XNU's
// sogetoptlock (bsd/kern/uipc_socket.c) has no case for SO_ACCEPTCONN and
// answers ENOPROTOOPT, although the flag is kept in so_options: the state is
// read instead from TCP_CONNECTION_INFO, whose first byte is tcpi_state, the
// tcpcb's t_state; sooptcopyout truncates the struct to the one byte asked.
func acceptsConnections(fd int) (bool, error) {
	state, err := syscall.GetsockoptByte(fd, syscall.IPPROTO_TCP, tcpConnectionInfo)
	return state == tcpStateListen, err
}
