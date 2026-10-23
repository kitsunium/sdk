// echo_client - the load generator for the echo baseline: N connections, each
// sending a fixed message and waiting for the full echo before the next, one
// epoll thread. It runs on another host than the server.
//
// usage: echo_client -h host -p port [-c conns] [-s bytes] [-t seconds] [-w warmup_seconds]
//
// Every echo is compared byte for byte with what was sent. Prints one result
// line; exits 1 when no round trip completed or any echo was wrong.
#include <arpa/inet.h>
#include <errno.h>
#include <netinet/in.h>
#include <netinet/tcp.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/epoll.h>
#include <sys/socket.h>
#include <unistd.h>

#include "cref.h"

struct conn {
	int fd;
	bool connected;
	bool out_armed;   // EPOLLOUT is in the interest set
	size_t off;       // bytes of the current message written
	size_t got;       // bytes of the echo read
	uint64_t sent_ns; // when the current message started
};

// set_out keeps EPOLLOUT in the interest set only while a write is unfinished,
// so a request costs the client no epoll_ctl.
static void set_out(int ep, struct conn *c, uint32_t idx, bool out)
{
	if (c->out_armed == out)
		return;
	struct epoll_event ev = {.events = out ? (EPOLLOUT | EPOLLIN) : EPOLLIN, .data.u32 = idx};
	epoll_ctl(ep, EPOLL_CTL_MOD, c->fd, &ev);
	c->out_armed = out;
}

static void die(const char *what)
{
	perror(what);
	exit(1);
}

int main(int argc, char **argv)
{
	const char *host = nullptr;
	int port = 9000, nconn = 64;
	size_t msg_len = 64;
	double secs = 10, warm = 2;

	for (int i = 1; i + 1 < argc; i += 2) {
		const char *k = argv[i], *v = argv[i + 1];
		if (strcmp(k, "-h") == 0)
			host = v;
		else if (strcmp(k, "-p") == 0)
			port = atoi(v);
		else if (strcmp(k, "-c") == 0)
			nconn = atoi(v);
		else if (strcmp(k, "-s") == 0)
			msg_len = strtoull(v, nullptr, 10);
		else if (strcmp(k, "-t") == 0)
			secs = atof(v);
		else if (strcmp(k, "-w") == 0)
			warm = atof(v);
		else {
			fprintf(stderr, "unknown option %s\n", k);
			return 2;
		}
	}
	if (host == nullptr || nconn <= 0 || msg_len == 0 || msg_len > 1024 || secs <= 0 || warm < 0) {
		fputs("usage: echo_client -h host -p port [-c conns] [-s bytes<=1024] [-t secs] [-w warmup]\n", stderr);
		return 2;
	}

	struct sockaddr_in addr = {.sin_family = AF_INET, .sin_port = htons((uint16_t)port)};
	if (inet_pton(AF_INET, host, &addr.sin_addr) != 1) {
		fprintf(stderr, "host must be an IPv4 address: %s\n", host);
		return 2;
	}

	char *msg = malloc(msg_len);
	char *rbuf = malloc((size_t)nconn * msg_len);
	struct conn *cs = calloc((size_t)nconn, sizeof *cs);
	if (msg == nullptr || rbuf == nullptr || cs == nullptr)
		die("alloc");
	for (size_t i = 0; i < msg_len; i++)
		msg[i] = (char)('a' + (i * 7) % 26);

	int ep = epoll_create1(EPOLL_CLOEXEC);
	if (ep < 0)
		die("epoll_create1");
	for (int i = 0; i < nconn; i++) {
		int fd = socket(AF_INET, SOCK_STREAM | SOCK_NONBLOCK | SOCK_CLOEXEC, 0);
		if (fd < 0)
			die("socket");
		int one = 1;
		setsockopt(fd, IPPROTO_TCP, TCP_NODELAY, &one, sizeof one);
		cs[i].fd = fd;
		if (connect(fd, (struct sockaddr *)&addr, sizeof addr) != 0 && errno != EINPROGRESS)
			die("connect");
		cs[i].out_armed = true;
		struct epoll_event ev = {.events = EPOLLOUT, .data.u32 = (uint32_t)i};
		if (epoll_ctl(ep, EPOLL_CTL_ADD, fd, &ev) != 0)
			die("epoll_ctl");
	}

	uint64_t t_start = cref_now_ns();
	uint64_t t_measure = t_start + (uint64_t)(warm * 1e9);
	uint64_t t_end = t_measure + (uint64_t)(secs * 1e9);
	uint64_t done = 0, bad = 0, lat_sum_ns = 0, io_errors = 0;
	uint64_t measure_begin_ns = 0;
	struct epoll_event evs[256];

	for (;;) {
		uint64_t now = cref_now_ns();
		if (now >= t_end)
			break;
		int n = epoll_wait(ep, evs, 256, 100);
		if (n < 0) {
			if (errno == EINTR)
				continue;
			die("epoll_wait");
		}
		for (int k = 0; k < n; k++) {
			struct conn *c = &cs[evs[k].data.u32];
			char *rb = rbuf + (size_t)evs[k].data.u32 * msg_len;

			if (c->fd < 0)
				continue;
			if (evs[k].events & (EPOLLERR | EPOLLHUP)) {
				io_errors++;
				close(c->fd);
				c->fd = -1;
				continue;
			}
			if (!c->connected) {
				int soerr = 0;
				socklen_t sl = sizeof soerr;
				getsockopt(c->fd, SOL_SOCKET, SO_ERROR, &soerr, &sl);
				if (soerr != 0) {
					io_errors++;
					close(c->fd);
					c->fd = -1;
					continue;
				}
				c->connected = true;
				c->off = c->got = 0;
				c->sent_ns = cref_now_ns();
			}
			if (c->off < msg_len) {
				ssize_t w = write(c->fd, msg + c->off, msg_len - c->off);
				if (w > 0)
					c->off += (size_t)w;
				else if (w < 0 && errno != EAGAIN && errno != EINTR) {
					io_errors++;
					close(c->fd);
					c->fd = -1;
					continue;
				}
				set_out(ep, c, evs[k].data.u32, c->off < msg_len);
			}
			if (evs[k].events & EPOLLIN) {
				ssize_t r = read(c->fd, rb + c->got, msg_len - c->got);
				if (r > 0)
					c->got += (size_t)r;
				else if (r == 0 || (errno != EAGAIN && errno != EINTR)) {
					io_errors++;
					close(c->fd);
					c->fd = -1;
					continue;
				}
				if (c->got == msg_len) {
					uint64_t t = cref_now_ns();
					if (t >= t_measure) {
						if (measure_begin_ns == 0)
							measure_begin_ns = t;
						done++;
						lat_sum_ns += t - c->sent_ns;
						if (memcmp(rb, msg, msg_len) != 0)
							bad++;
					}
					c->off = c->got = 0;
					c->sent_ns = t;
				}
			}
			if (c->connected && c->off == 0 && c->got == 0) {
				ssize_t w = write(c->fd, msg, msg_len);
				if (w > 0)
					c->off = (size_t)w;
				else if (w < 0 && errno != EAGAIN && errno != EINTR) {
					io_errors++;
					close(c->fd);
					c->fd = -1;
					continue;
				}
				set_out(ep, c, evs[k].data.u32, c->off < msg_len);
			}
		}
	}

	double window = (double)(cref_now_ns() - (measure_begin_ns ? measure_begin_ns : t_measure)) / 1e9;
	printf("bench=echo_client conns=%d msg_bytes=%zu completed=%llu window_s=%.3f rps=%.1f "
	       "mean_latency_us=%.1f mismatches=%llu io_errors=%llu\n",
	       nconn, msg_len, (unsigned long long)done, window, window > 0 ? (double)done / window : 0.0,
	       done ? (double)lat_sum_ns / (double)done / 1000.0 : 0.0, (unsigned long long)bad,
	       (unsigned long long)io_errors);
	free(cs);
	free(rbuf);
	free(msg);
	return (done == 0 || bad != 0) ? 1 : 0;
}
