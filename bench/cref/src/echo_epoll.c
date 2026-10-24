// echo_epoll - a level-triggered epoll echo server, the kernel baseline the
// io_uring one is paired with. One thread, one read and one write per
// request, no HTTP.
//
// usage: echo_epoll [-p port]
//
// SIGUSR1 starts the measured window (the request count and the CPU time are
// taken as a baseline); SIGTERM or SIGINT stops and prints one result line.
#include <errno.h>
#include <netinet/in.h>
#include <netinet/tcp.h>
#include <signal.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/epoll.h>
#include <sys/socket.h>
#include <unistd.h>

#include "cref.h"

constexpr int MAX_EVENTS = 512;
constexpr int ACCEPT_BATCH = 128;
constexpr size_t IOBUF = 4096;

static volatile sig_atomic_t g_stop;
static volatile sig_atomic_t g_reset;

static void on_term(int sig)
{
	(void)sig;
	g_stop = 1;
}

static void on_usr1(int sig)
{
	(void)sig;
	g_reset = 1;
}

// A connection whose echo could not be written in full parks the rest here and
// stops reading until it drains: backpressure, not buffering without bound.
struct pending {
	size_t off;
	size_t len;
	char data[IOBUF];
};

static struct pending **g_pending; // by fd
static size_t g_pending_cap;

static void die(const char *what)
{
	perror(what);
	exit(1);
}

static int g_ep = -1;
static int g_lfd = -1;
static bool g_listener_off;

static void close_conn(int fd)
{
	if (g_listener_off) { // a descriptor is free again: listen once more
		struct epoll_event le = {.events = EPOLLIN, .data.fd = g_lfd};
		epoll_ctl(g_ep, EPOLL_CTL_MOD, g_lfd, &le);
		g_listener_off = false;
	}
	if ((size_t)fd < g_pending_cap) {
		free(g_pending[fd]);
		g_pending[fd] = nullptr;
	}
	close(fd); // closing removes it from the epoll set
}

int main(int argc, char **argv)
{
	int port = 9000;

	for (int i = 1; i + 1 < argc; i += 2) {
		if (strcmp(argv[i], "-p") == 0)
			port = atoi(argv[i + 1]);
		else {
			fprintf(stderr, "unknown option %s\n", argv[i]);
			return 2;
		}
	}

	struct sigaction sa = {.sa_handler = on_term};
	sigaction(SIGTERM, &sa, nullptr);
	sigaction(SIGINT, &sa, nullptr);
	sa.sa_handler = on_usr1;
	sigaction(SIGUSR1, &sa, nullptr);
	signal(SIGPIPE, SIG_IGN);

	g_pending_cap = 1u << 20;
	g_pending = (struct pending **)calloc(g_pending_cap, sizeof *g_pending);
	if (g_pending == nullptr)
		die("calloc");

	int lfd = socket(AF_INET, SOCK_STREAM | SOCK_NONBLOCK | SOCK_CLOEXEC, 0);
	if (lfd < 0)
		die("socket");
	int one = 1;
	setsockopt(lfd, SOL_SOCKET, SO_REUSEADDR, &one, sizeof one);
	setsockopt(lfd, IPPROTO_TCP, TCP_NODELAY, &one, sizeof one); // inherited by accepted sockets
	struct sockaddr_in addr = {.sin_family = AF_INET, .sin_port = htons((uint16_t)port)};
	if (bind(lfd, (struct sockaddr *)&addr, sizeof addr) != 0)
		die("bind");
	if (listen(lfd, 4096) != 0)
		die("listen");

	int ep = epoll_create1(EPOLL_CLOEXEC);
	if (ep < 0)
		die("epoll_create1");
	struct epoll_event ev = {.events = EPOLLIN, .data.fd = lfd};
	if (epoll_ctl(ep, EPOLL_CTL_ADD, lfd, &ev) != 0)
		die("epoll_ctl");
	g_ep = ep;
	g_lfd = lfd;

	uint64_t requests = 0, conns = 0, errors = 0;
	uint64_t base_requests = 0;
	struct cref_cpu base_cpu = cref_cpu_now();
	uint64_t base_ns = cref_now_ns();
	char buf[IOBUF];
	struct epoll_event evs[MAX_EVENTS];

	while (!g_stop) {
		if (g_reset) {
			g_reset = 0;
			base_requests = requests;
			base_cpu = cref_cpu_now();
			base_ns = cref_now_ns();
		}
		// 100 ms, not -1: a SIGTERM landing between the g_stop test and the wait
		// is then seen within a tick instead of never.
		int n = epoll_wait(ep, evs, MAX_EVENTS, 100);
		if (n < 0) {
			if (errno == EINTR)
				continue;
			die("epoll_wait");
		}
		for (int k = 0; k < n; k++) {
			int fd = evs[k].data.fd;

			if (fd == lfd) {
				for (int a = 0; a < ACCEPT_BATCH; a++) {
					int c = accept4(lfd, nullptr, nullptr, SOCK_NONBLOCK | SOCK_CLOEXEC);
					if (c < 0) {
						if (errno == EMFILE || errno == ENFILE) {
							// Level-triggered: leave the set until a close frees a fd.
							struct epoll_event off = {.events = 0, .data.fd = lfd};
							epoll_ctl(ep, EPOLL_CTL_MOD, lfd, &off);
							g_listener_off = true;
							errors++;
						} else if (errno != EAGAIN && errno != EINTR && errno != ECONNABORTED)
							errors++;
						break;
					}
					if ((size_t)c >= g_pending_cap) {
						close(c);
						errors++;
						continue;
					}
					struct epoll_event ce = {.events = EPOLLIN, .data.fd = c};
					if (epoll_ctl(ep, EPOLL_CTL_ADD, c, &ce) != 0) {
						close(c);
						errors++;
						continue;
					}
					conns++;
				}
				continue;
			}

			struct pending *p = g_pending[fd];
			if (p != nullptr && (evs[k].events & (EPOLLERR | EPOLLHUP))) {
				close_conn(fd);
				continue;
			}
			if (p != nullptr && (evs[k].events & EPOLLOUT)) {
				ssize_t w = write(fd, p->data + p->off, p->len - p->off);
				if (w < 0 && errno != EAGAIN && errno != EINTR) {
					close_conn(fd);
					continue;
				}
				if (w > 0)
					p->off += (size_t)w;
				if (p->off == p->len) {
					free(p);
					g_pending[fd] = nullptr;
					struct epoll_event me = {.events = EPOLLIN, .data.fd = fd};
					epoll_ctl(ep, EPOLL_CTL_MOD, fd, &me);
				}
				continue;
			}
			if (p != nullptr)
				continue; // draining: ignore reads until the echo is out

			ssize_t r = read(fd, buf, sizeof buf);
			if (r > 0) {
				requests++;
				ssize_t w = write(fd, buf, (size_t)r);
				if (w < 0 && errno != EAGAIN && errno != EINTR) {
					close_conn(fd);
					continue;
				}
				size_t sent = w > 0 ? (size_t)w : 0;
				if (sent < (size_t)r) {
					p = malloc(sizeof *p);
					if (p == nullptr) {
						close_conn(fd);
						errors++;
						continue;
					}
					p->off = 0;
					p->len = (size_t)r - sent;
					memcpy(p->data, buf + sent, p->len);
					g_pending[fd] = p;
					struct epoll_event me = {.events = EPOLLOUT, .data.fd = fd};
					epoll_ctl(ep, EPOLL_CTL_MOD, fd, &me);
				}
			} else if (r == 0 || (errno != EAGAIN && errno != EINTR)) {
				close_conn(fd);
			}
		}
	}

	struct cref_cpu now = cref_cpu_now();
	uint64_t reqs = requests - base_requests;
	double u = now.utime_us - base_cpu.utime_us;
	double s = now.stime_us - base_cpu.stime_us;
	double rq = reqs ? (double)reqs : 1.0;
	printf("bench=echo_epoll requests=%llu conns_total=%llu errors=%llu window_s=%.3f utime_us=%.0f "
	       "stime_us=%.0f utime_us_per_req=%.5f stime_us_per_req=%.5f cpu_us_per_req=%.5f\n",
	       (unsigned long long)reqs, (unsigned long long)conns,
	       (unsigned long long)errors, (double)(cref_now_ns() - base_ns) / 1e9, u, s, u / rq,
	       s / rq, (u + s) / rq);
	return 0;
}
